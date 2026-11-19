package ontospec

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

const zoo = `
id: zoo
version: 2
classes:
  Animal:
    description: A living animal.
    properties:
      name_tag: {type: string, pattern: "^[a-z]+$"}
      age: {type: integer, required: true}
  Dog:
    subClassOf: Animal
    aliases: [hound, canine]
    properties:
      breed: {type: string, enum: [lab, poodle]}
  Puppy:
    subClassOf: Dog
  Keeper: {}
relations:
  keeps:
    domain: Keeper
    range: [Dog, Cat]
    temporal: true
    aliases: [looks_after]
    properties:
      since: {type: date}
  chases:
    domain: Dog
    range: Animal
`

func mustCompile(t *testing.T,
	doc string) *Registry {
	t.Helper()
	// "Cat" is not defined: the zoo has to be completed for the tests that need a valid ontology.
	doc = strings.Replace(doc, "range: [Dog, Cat]", "range: [Dog]", 1)
	spec, err := Parse(FormatYAML, []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Compile(Part{
		Ref:  Ref{ID: 7, Name: "zoo", Version: 3},
		Spec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestParseYAMLAndJSON(t *testing.T) {
	spec, err := Parse(FormatYAML, []byte(zoo))
	if err != nil {
		t.Fatal(err)
	}
	if spec.ID != "zoo" || spec.Version != "2" || len(spec.Classes) != 4 || len(spec.Relations) != 2 {
		t.Fatalf("spec = %+v", spec)
	}
	if got := spec.Relations["keeps"].Range; len(got) != 2 || got[0] != "Dog" {
		t.Errorf("range = %v", got)
	}
	if got := spec.Classes["Dog"].SubClassOf; len(got) != 1 || got[0] != "Animal" {
		t.Errorf("a single parent must read as a list: %v", got)
	}
	asJSON, _ := json.Marshal(spec)
	again, err := Parse(DetectFormat(asJSON), asJSON)
	if err != nil || again.Classes["Puppy"].SubClassOf[0] != "Dog" {
		t.Fatalf("JSON round trip: %v %+v", err, again)
	}
	for _, bad := range []string{"classes: [", "classes:\n  A:\n    nope: 1", "classes:\n  A:\n    subClassOf: 5"} {
		if _, err := Parse(FormatYAML, []byte(bad)); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	if _, err := Parse("xml", []byte("<a/>")); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown format: %v", err)
	}
}

func problems(t *testing.T,
	doc string) string {
	t.Helper()
	spec, err := Parse(FormatYAML, []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range Validate(spec) {
		out = append(out, p.String())
	}
	return strings.Join(out, "\n")
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"empty", "id: x", "classes: the ontology defines no classes"},
		{"unknown parent", "classes:\n  A:\n    subClassOf: B", `unknown class "B"`},
		{"self parent", "classes:\n  A:\n    subClassOf: A", "its own parent"},
		{"cycle", "classes:\n  A:\n    subClassOf: B\n  B:\n    subClassOf: C\n  C:\n    subClassOf: A", "classes.B.subClassOf: subClassOf cycle through B"},
		{"unknown domain", "classes:\n  A: {}\nrelations:\n  r:\n    domain: Z", `relations.r.domain: unknown class "Z"`},
		{"unknown range", "classes:\n  A: {}\nrelations:\n  r:\n    range: [A, Z]", `relations.r.range: unknown class "Z"`},
		{"unknown type", "classes:\n  A:\n    properties:\n      p: {type: blob}", "unknown type"},
		{"bad pattern", "classes:\n  A:\n    properties:\n      p: {type: string, pattern: '('}", "not a valid regular expression"},
		{"enum on number", "classes:\n  A:\n    properties:\n      p: {type: number, enum: [a]}", "only for the type string"},
		{"alias collision", "classes:\n  A:\n    aliases: [x]\n  B:\n    aliases: [X]", `already used by "A"`},
		{"alias is a name", "classes:\n  A: {}\n  B:\n    aliases: [a]", `already used by "A"`},
		{"name collision by key", "classes:\n  MarketThing: {}\n  market_thing: {}", "already used"},
		{"relation alias collision", "classes:\n  A: {}\nrelations:\n  r:\n    aliases: [s]\n  s: {}", `already used by "r"`},
		{"redefined property", "classes:\n  A:\n    properties:\n      p: {type: string}\n  B:\n    subClassOf: A\n    properties:\n      p: {type: number}", "redefines the property"},
	} {
		if got := problems(t, tc.doc); !strings.Contains(got, tc.want) {
			t.Errorf("%s: problems = %q, want %q", tc.name, got, tc.want)
		}
	}
	// The same name as a class and as a relation is fine, and a subclass may make an inherited property required.
	if got := problems(t, "classes:\n  A:\n    properties:\n      p: {type: string}\n  B:\n    subClassOf: A\n    properties:\n      p: {type: string, required: true}\nrelations:\n  A: {}"); got != "" {
		t.Errorf("a valid ontology has problems: %s", got)
	}
	var invalid *InvalidError
	if _, err := Compile(Part{Spec: Spec{}}); !errors.As(err, &invalid) || !errors.Is(err, ErrInvalid) || len(invalid.Problems) == 0 {
		t.Errorf("Compile of an invalid spec: %v", err)
	}
}

func TestCompileHierarchyAndAliases(t *testing.T) {
	reg := mustCompile(t, zoo)
	if got := strings.Join(reg.Ancestors("puppy"), ","); got != "Dog,Animal" {
		t.Errorf("ancestors = %s", got)
	}
	if got := strings.Join(reg.Descendants("Animal"), ","); got != "Dog,Puppy" {
		t.Errorf("descendants = %s", got)
	}
	if got := strings.Join(reg.Expand("hound"), ","); got != "Dog,Puppy" {
		t.Errorf("expand = %s", got)
	}
	if !reg.IsSubClassOf("Puppy", "animal") || reg.IsSubClassOf("Animal", "Puppy") || !reg.IsSubClassOf("Dog", "Dog") || reg.IsSubClassOf("Keeper", "Animal") || reg.IsSubClassOf("x", "Animal") {
		t.Error("IsSubClassOf")
	}
	if c, alias, ok := reg.Resolve("Canine"); c != "Dog" || !alias || !ok {
		t.Errorf("alias: %q %v %v", c, alias, ok)
	}
	if c, alias, ok := reg.Resolve("dog"); c != "Dog" || alias || !ok {
		t.Errorf("name by another case: %q %v %v", c, alias, ok)
	}
	if r, alias, ok := reg.ResolveRelation("looks after"); r != "keeps" || !alias || !ok {
		t.Errorf("relation alias: %q %v %v", r, alias, ok)
	}
	if _, _, ok := reg.Resolve("cat"); ok {
		t.Error("cat is not a class")
	}
	if ref := reg.ClassRef("Dog"); ref.Name != "zoo" || ref.Version != 3 || ref.ID != 7 {
		t.Errorf("ref = %+v", ref)
	}
	// A subclass has the properties of its ancestors.
	if _, ok := reg.classes["Puppy"].props["age"]; !ok || !reg.classes["Puppy"].props["age"].def.Required {
		t.Error("Puppy lacks the inherited property age")
	}
}

func TestExtractionJSON(t *testing.T) {
	reg := mustCompile(t, zoo)
	b, err := json.Marshal(reg.ExtractionJSON())
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("schema = %s", b)
	}
	entities := schema["properties"].(map[string]any)["entities"].(map[string]any)["items"].(map[string]any)["anyOf"].([]any)
	edges := schema["properties"].(map[string]any)["edges"].(map[string]any)["items"].(map[string]any)["anyOf"].([]any)
	if len(entities) != 4 || len(edges) != 2 {
		t.Fatalf("%d entity and %d edge branches", len(entities), len(edges))
	}
	for _, want := range []string{`"const":"Puppy"`, `"required":["age"]`, `"enum":["lab","poodle"]`, `"valid_at"`, `"additionalProperties":false`, `"const":"keeps"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("schema lacks %s", want)
		}
	}
	// Only the temporal relation carries valid_at / invalid_at.
	for _, e := range edges {
		props := e.(map[string]any)["properties"].(map[string]any)
		_, temporal := props["valid_at"]
		if want := props["relation"].(map[string]any)["const"] == "keeps"; temporal != want {
			t.Errorf("relation %v: temporal fields = %v", props["relation"], temporal)
		}
	}
	if p := reg.Prompt(); !strings.Contains(p, "- Dog (a kind of Animal)") || !strings.Contains(p, "keeps (source: Keeper; target: Dog)") || !strings.Contains(p, "property age: integer, required") {
		t.Errorf("prompt:\n%s", p)
	}
}

func TestMerge(t *testing.T) {
	a, _ := Parse(FormatYAML, []byte("classes:\n  A:\n    description: one\n  B: {}\nrelations:\n  r:\n    domain: A\n    range: B"))
	b, _ := Parse(FormatYAML, []byte("classes:\n  B: {}\n  C:\n    subClassOf: A"))
	reg, err := Compile(Part{Ref: Ref{Name: "a"}, Spec: a}, Part{Ref: Ref{Name: "b"}, Spec: b})
	if err != nil {
		t.Fatal(err)
	}
	// B is defined identically in both: it is one class. C is a subclass of a class of the other ontology.
	if !reg.IsSubClassOf("C", "A") || len(reg.Classes()) != 3 || reg.ClassRef("A").Name != "a" || reg.ClassRef("C").Name != "b" || reg.ClassRef("B").Name != "a" {
		t.Errorf("merged: %v", reg.Classes())
	}
	clash, _ := Parse(FormatYAML, []byte("classes:\n  A:\n    description: two"))
	_, err = Compile(Part{Ref: Ref{Name: "a"}, Spec: a}, Part{Ref: Ref{Name: "clash"}, Spec: clash})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `"a" and "clash"`) {
		t.Errorf("conflict error = %v", err)
	}
	if _, err := Compile(Part{Ref: Ref{Name: "a"}, Spec: a}, Part{Ref: Ref{Name: "again"}, Spec: a}); err != nil {
		t.Errorf("the same definition twice must merge: %v", err)
	}
}

func ground(t *testing.T,
	mode string,
	nodes []Node,
	links []Link) []Item {
	t.Helper()
	return mustCompile(t, zoo).Ground(mode, nodes, links)
}

func outcomes(items []Item) string {
	return strings.Join(mapSlice(items, func(i Item) string {
		return i.Name + "=" + i.Outcome + ":" + i.Reason
	}), " ")
}

func TestGroundAnnotate(t *testing.T) {
	items := ground(t, ModeAnnotate,
		[]Node{
			{Name: "Rex", Type: "dog", Properties: map[string]any{"age": 3.0, "breed": "lab"}},
			{Name: "Pip", Type: "Hound", Properties: map[string]any{"age": 1.0}},
			{Name: "Tom", Type: "cat"},
			{Name: "Old", Type: "dog"},
			{Name: "Bad", Type: "dog", Properties: map[string]any{"age": 1.5, "breed": "pug", "color": "red"}},
			{Name: "Sam", Type: "keeper"},
		},
		[]Link{
			{Source: "Sam", Target: "Rex", Relation: "keeps", Properties: map[string]any{"since": "2020-01-31"}},
			{Source: "Sam", Target: "Pip", Relation: "looks_after"},
			{Source: "Rex", Target: "Sam", Relation: "keeps"},
			{Source: "Sam", Target: "Tom", Relation: "keeps"},
			{Source: "Sam", Target: "Rex", Relation: "adores"},
			{Source: "Sam", Target: "Rex", Relation: "keeps", Properties: map[string]any{"since": "yesterday"}},
			{Source: "Sam", Target: "Nobody", Relation: "keeps"},
		})
	want := []string{
		"Rex=matched", "Pip=matched_via_alias", "Tom=unmatched:unknown_class", "Old=violation:missing_required",
		"Bad=violation:property_invalid", "Sam=matched",
		"Sam keeps Rex=matched", "Sam looks_after Pip=matched_via_alias", "Rex keeps Sam=violation:domain_violation",
		"Sam keeps Tom=violation:range_violation", "Sam adores Rex=unmatched:unknown_relation",
		"Sam keeps Rex=violation:property_invalid", "Sam keeps Nobody=matched", // an end the extraction does not list is not judged
	}
	if got := strings.TrimSuffix(norm(outcomes(items)), ":"); got != strings.Join(want, " ") {
		t.Errorf("annotate:\n got %s\nwant %s", got, strings.Join(want, " "))
	}
	for _, it := range items {
		if !it.Kept() {
			t.Errorf("annotate dropped %s", it.Name)
		}
	}
	if items[0].Class != "Dog" || items[0].Ref.Name != "zoo" || items[0].Ref.Version != 3 || !items[0].Valid() {
		t.Errorf("item = %+v", items[0])
	}
	var issues []Issue
	if v, ok := items[4].Detail["issues"].([]Issue); ok {
		issues = v
	}
	if len(issues) != 3 { // age is not an integer, breed is not allowed, color is not a property
		t.Errorf("issues = %+v", items[4].Detail)
	}
}

// norm drops the trailing colon of an outcome with no reason.
func norm(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "matched: ", "matched "), "matched_via_alias: ", "matched_via_alias ")
}

func TestGroundStrict(t *testing.T) {
	items := ground(t, ModeStrict,
		[]Node{
			{Name: "Rex", Type: "dog", Properties: map[string]any{"age": 3.0}},
			{Name: "Tom", Type: "cat"},
			{Name: "Sam", Type: "keeper"},
		},
		[]Link{
			{Source: "Sam", Target: "Rex", Relation: "keeps"},
			{Source: "Sam", Target: "Tom", Relation: "keeps"},
			{Source: "Sam", Target: "Ghost", Relation: "keeps"},
			{Source: "Sam", Target: "Rex", Relation: "adores"},
		})
	want := "Rex=matched Tom=dropped:unknown_class Sam=matched Sam keeps Rex=matched Sam keeps Tom=dropped:endpoint_dropped " +
		"Sam keeps Ghost=dropped:endpoint_unlisted Sam adores Rex=dropped:unknown_relation"
	if got := strings.TrimSuffix(norm(outcomes(items)+" "), " "); got != want {
		t.Errorf("strict:\n got %s\nwant %s", got, want)
	}
	sum := Summarize(ModeStrict, []Ref{{Name: "zoo", Version: 3}}, items).(StrictSummary)
	if sum.Nodes.Total != 3 || sum.Nodes.Dropped != 1 || sum.Edges.Total != 4 || sum.Edges.Dropped != 3 || sum.DroppedRatio < 0.571 || sum.DroppedRatio > 0.572 || len(sum.Violations) != 4 {
		t.Errorf("summary = %+v", sum)
	}
}

func TestSummarizeAnnotate(t *testing.T) {
	items := ground(t, ModeAnnotate,
		[]Node{{Name: "Rex", Type: "dog", Properties: map[string]any{"age": 1.0}}, {Name: "Pip", Type: "hound", Properties: map[string]any{"age": 1.0}}, {Name: "Tom", Type: "cat"}, {Name: "Sam", Type: "keeper"}},
		[]Link{{Source: "Sam", Target: "Rex", Relation: "keeps"}, {Source: "Sam", Target: "Tom", Relation: "hugs"}})
	sum := Summarize(ModeAnnotate, []Ref{{Name: "zoo", Version: 3}}, items).(AnnotateSummary)
	if sum.Mode != ModeAnnotate || sum.Nodes != (Counts{Total: 4, Matched: 2, ViaAlias: 1, Violations: 1}) || sum.Edges != (Counts{Total: 2, Matched: 1, Violations: 1}) {
		t.Errorf("counts = %+v", sum)
	}
	if sum.MatchedPercent < 66.6 || sum.MatchedPercent > 66.7 || len(sum.MatchedClasses) != 3 || sum.MatchedClasses[0].Class != "Dog" || sum.MatchedClasses[0].Count != 2 {
		t.Errorf("summary = %+v", sum)
	}
	if len(sum.Violations) != 2 {
		t.Errorf("violations = %+v", sum.Violations)
	}
	if empty := Summarize(ModeAnnotate, nil, nil).(AnnotateSummary); empty.MatchedPercent != 0 {
		t.Errorf("empty = %+v", empty)
	}
}

func TestPropertyValues(t *testing.T) {
	reg := mustCompile(t, `
classes:
  T:
    properties:
      s: {type: string}
      n: {type: number}
      i: {type: integer}
      b: {type: boolean}
      d: {type: date}
`)
	ok := map[string]any{"s": "x", "n": 1.5, "i": 2.0, "b": true, "d": "2024-02-29"}
	if got := reg.Ground(ModeStrict, []Node{{Name: "t", Type: "t", Properties: ok}}, nil); !got[0].Valid() {
		t.Errorf("valid values refused: %+v", got[0])
	}
	if got := reg.Ground(ModeStrict, []Node{{Name: "t", Type: "t", Properties: map[string]any{"d": "2024-01-01T10:00:00Z", "s": nil}}}, nil); !got[0].Valid() {
		t.Errorf("a date-time and a null must be accepted: %+v", got[0])
	}
	for _, bad := range []map[string]any{{"s": 1.0}, {"n": "1"}, {"i": 2.5}, {"b": "true"}, {"d": "2023-02-29"}, {"d": 5.0}} {
		if got := reg.Ground(ModeStrict, []Node{{Name: "t", Type: "t", Properties: bad}}, nil); got[0].Valid() || got[0].Reason != ReasonPropertyInvalid {
			t.Errorf("%v accepted: %+v", bad, got[0])
		}
	}
}

func TestMarketOntology(t *testing.T) {
	doc, err := os.ReadFile("../../../../build/ontology/market.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := Parse(FormatYAML, doc)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Compile(Part{Ref: Ref{Name: "market", Version: 1}, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Classes()) != 20 || len(reg.Relations()) != 26 {
		t.Errorf("%d classes, %d relations", len(reg.Classes()), len(reg.Relations()))
	}
	if !reg.IsSubClassOf("Exchange", "Market") || !reg.IsSubClassOf("stock", "Asset") || reg.IsSubClassOf("Market", "Exchange") {
		t.Error("market hierarchy")
	}
	got := reg.Ground(ModeStrict,
		[]Node{{Name: "NYSE", Type: "exchange"}, {Name: "ACME", Type: "security", Properties: map[string]any{"ticker": "ACME"}}},
		[]Link{{Source: "ACME", Target: "NYSE", Relation: "traded_on"}, {Source: "NYSE", Target: "ACME", Relation: "tradedOn"}})
	if !got[0].Valid() || !got[1].Valid() || !got[2].Valid() || got[3].Reason != ReasonDomainViolation {
		t.Errorf("market grounding: %s", outcomes(got))
	}
}
