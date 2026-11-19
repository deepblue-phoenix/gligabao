package memory_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_ontology/ontospec"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

const keepersOntology = `
classes:
  Animal:
    properties:
      age: {type: integer, required: true}
  Dog:
    subClassOf: Animal
    aliases: [hound]
  Keeper: {}
relations:
  keeps:
    domain: Keeper
    range: Animal
    temporal: true
`

// ontoLLM answers the extraction with a fixed graph (Sam keeps Rex and Pip, Sam keeps the cat Tom, who is not in the ontology, and
// Sam adores Rex, which is not a relation of it), and keeps the system prompts of the extractions.
type ontoLLM struct {
	systems []string
}

func (l *ontoLLM) Complete(_ context.Context,
	req llm.Request) (*llm.Response, error) {
	if !strings.Contains(req.System, "extract a temporal knowledge graph") {
		return &llm.Response{
			StopReason: "end_turn",
			Model:      "onto-model",
			Text:       `{"duplicates":[],"contradictions":[]}`,
		}, nil
	}
	l.systems = append(l.systems, req.System)
	return &llm.Response{
		StopReason: "end_turn",
		Model:      "onto-model",
		Text: `{"entities":[
			{"name":"Rex","type":"Dog","properties":{"age":3}},
			{"name":"Pip","type":"hound","properties":{"age":1}},
			{"name":"Max","type":"dog"},
			{"name":"Tom","type":"cat","properties":{"age":2}},
			{"name":"Sam","type":"keeper"}],
		"edges":[
			{"source":"Sam","target":"Rex","relation":"keeps","description":"Sam keeps Rex."},
			{"source":"Sam","target":"Tom","relation":"keeps","description":"Sam keeps Tom."},
			{"source":"Sam","target":"Rex","relation":"adores","description":"Sam adores Rex."}]}`,
	}, nil
}

func TestRememberOntologyGrounding(t *testing.T) { dbtest.Run(t, testRememberOntologyGrounding) }

func testRememberOntologyGrounding(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	onto, err := memory_ontology.Create(ctx, d, "u1", "keepers", "", "", []byte(keepersOntology))
	if err != nil {
		t.Fatal(err)
	}

	type stored struct {
		class   sql.NullString
		version sql.NullInt64
		valid   sql.NullBool
		origin  sql.NullString
	}
	entityTags := func(dataset string) map[string]stored {
		t.Helper()
		rows, err := d.QueryContext(ctx, `
			SELECT e.name, e.ontology_class, e.ontology_version, e.ontology_valid, e.origin_type
			FROM gb_entities e JOIN gb_datasets ds ON ds.id = e.dataset_id
			WHERE ds.name = '`+dataset+`'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]stored{}
		for rows.Next() {
			var name string
			var s stored
			if err := rows.Scan(&name, &s.class, &s.version, &s.valid, &s.origin); err != nil {
				t.Fatal(err)
			}
			out[name] = s
		}
		return out
	}

	//------------------------------------------------------------------
	// annotate: everything is stored; the matches are tagged, the rest flagged
	l := &ontoLLM{}
	res, err := memory_remember.Remember(ctx, d, l, "u1", "annotated", "", "Sam keeps Rex, Pip and Tom.", relational.Now(),
		memory_core.WithOntologyGrounding(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.systems) != 1 || !strings.Contains(l.systems[0], "<ontology>") || !strings.Contains(l.systems[0], "- Dog (a kind of Animal)") || strings.Contains(l.systems[0], "json_schema") {
		t.Errorf("annotate prompt:\n%s", l.systems[0])
	}
	sum, ok := res.OntologyGrounding.(ontospec.AnnotateSummary)
	if !ok {
		t.Fatalf("summary = %T", res.OntologyGrounding)
	}
	// Rex and Sam match, Pip matches by its alias; Tom is not a class and Max lacks his required age. Of the edges, "keeps Rex" matches;
	// "keeps Tom" has a range that is not a class (a violation) and "adores" is not a relation.
	if sum.Mode != "annotate" || len(sum.Ontologies) != 1 || sum.Ontologies[0].Name != "keepers" || sum.Ontologies[0].Version != 1 ||
		sum.Nodes != (ontospec.Counts{Total: 5, Matched: 2, ViaAlias: 1, Violations: 2}) || sum.Edges != (ontospec.Counts{Total: 3, Matched: 1, Violations: 2}) {
		t.Errorf("summary = %+v", sum)
	}
	// Nothing is dropped; the neighborhood of Dog and Keeper is added: the entities Dog, Keeper and Animal, an is_a edge for each of Rex, Pip
	// and Sam, Dog subclass_of Animal, and the relation Keeper keeps Animal (it runs between two classes of the subgraph).
	if len(res.Entities) != 8 || len(res.Edges) != 8 {
		t.Errorf("annotate dropped something: %d entities, %d edges", len(res.Entities), len(res.Edges))
	}
	tags := entityTags("annotated")
	if got := tags["Rex"]; got.class.String != "Dog" || got.version.Int64 != 1 || !got.valid.Bool {
		t.Errorf("Rex = %+v", got)
	}
	if tags["Rex"].origin.String != "extracted" || tags["Animal"].origin.String != "ontology" || tags["Dog"].origin.String != "ontology" {
		t.Errorf("origins: Rex %q, Animal %q, Dog %q", tags["Rex"].origin.String, tags["Animal"].origin.String, tags["Dog"].origin.String)
	}
	if got := tags["Pip"]; got.class.String != "Dog" || !got.valid.Bool {
		t.Errorf("Pip (by an alias) = %+v", got)
	}
	if got := tags["Max"]; got.class.String != "Dog" || got.valid.Bool {
		t.Errorf("Max (no age) = %+v", got)
	}
	if got := tags["Tom"]; got.class.Valid || !got.version.Valid || got.valid.Bool {
		t.Errorf("Tom (unknown class) = %+v", got)
	}
	if got := tags["Sam"]; got.class.String != "Keeper" || !got.valid.Bool {
		t.Errorf("Sam = %+v", got)
	}
	events, err := memory_ontology.Events(ctx, d, "u1", "keepers", graph.OntologyEventFilter{ArtifactID: res.ArtifactID})
	if err != nil || len(events) != 8 {
		t.Fatalf("%d events (%v), want one per node and edge", len(events), err)
	}
	byName := map[string]graph.OntologyEvent{}
	for _, e := range events {
		byName[e.SubjectKind+":"+e.Name] = e
		if e.OntologyID != onto.ID || e.OntologyVersion != 1 || e.Mode != "annotate" || e.ArtifactID != res.ArtifactID || e.RunID != res.RememberRunID || e.CreatedAt.IsZero() {
			t.Errorf("event = %+v", e)
		}
	}
	if e := byName["node:Max"]; e.Outcome != "violation" || e.Reason != "missing_required" || e.EntityID == 0 || !strings.Contains(string(e.Detail), `"age"`) {
		t.Errorf("Max event = %+v %s", e, e.Detail)
	}
	if e := byName["node:Pip"]; e.Outcome != "matched_via_alias" || e.Class != "Dog" {
		t.Errorf("Pip event = %+v", e)
	}
	if e := byName["node:Tom"]; e.Outcome != "unmatched" || e.Reason != "unknown_class" || e.Class != "" || e.EntityID == 0 {
		t.Errorf("Tom event = %+v", e)
	}
	if e := byName["edge:Sam adores Rex"]; e.Outcome != "unmatched" || e.Reason != "unknown_relation" || e.EdgeID == 0 {
		t.Errorf("adores event = %+v", e)
	}
	if e := byName["edge:Sam keeps Rex"]; e.Outcome != "matched" || e.Class != "keeps" || e.EdgeID == 0 {
		t.Errorf("keeps event = %+v", e)
	}
	if only, _ := memory_ontology.Events(ctx, d, "u1", "keepers", graph.OntologyEventFilter{Outcome: "violation"}); len(only) != 2 {
		t.Errorf("%d violation events, want 2 (Max and Sam keeps Tom)", len(only))
	}
	// The edges carry the same tags: one is a valid keeps; "keeps Tom" resolved to the relation but is a violation; "adores" resolved to nothing.
	rows, err := d.QueryContext(ctx, `SELECT ontology_class, ontology_valid, origin_type FROM gb_edges ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var edgeTags []string
	for rows.Next() {
		var class sql.NullString
		var valid sql.NullBool
		var origin sql.NullString
		if err := rows.Scan(&class, &valid, &origin); err != nil {
			t.Fatal(err)
		}
		edgeTags = append(edgeTags, class.String+"/"+map[bool]string{true: "valid", false: "invalid"}[valid.Bool]+"/"+origin.String)
	}
	rows.Close()
	if strings.Join(edgeTags, " ") != "keeps/valid/extracted keeps/invalid/extracted /invalid/extracted is_a/valid/ontology is_a/valid/ontology is_a/valid/ontology subclass_of/valid/ontology keeps/valid/ontology" {
		t.Errorf("edge tags = %v", edgeTags)
	}
	runs, _, _ := relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{}, 10, 0)
	if st := runs[0].Stages[relational.RememberStageGroundGraph]; st.Status != relational.RememberSucceeded {
		t.Errorf("ground_graph stage = %+v", st)
	}

	//------------------------------------------------------------------
	// strict: what does not conform is dropped before it is stored
	l = &ontoLLM{}
	res, err = memory_remember.Remember(ctx, d, l, "u1", "strict", "", "Sam keeps Rex, Pip and Tom.", relational.Now(),
		memory_core.WithOntologyGrounding([]string{"keepers"}, "strict"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(l.systems[0], "<json_schema>") || !strings.Contains(l.systems[0], `"const":"Dog"`) {
		t.Errorf("strict prompt lacks the schema:\n%s", l.systems[0])
	}
	strict, ok := res.OntologyGrounding.(ontospec.StrictSummary)
	if !ok {
		t.Fatalf("summary = %T", res.OntologyGrounding)
	}
	// Max (a required property is missing) and Tom are dropped, and so are the edges of Tom and "adores"; Sam keeps Rex stays.
	if strict.Mode != "strict" || strict.Nodes.Dropped != 2 || strict.Edges.Dropped != 2 || strict.DroppedRatio != 0.5 || len(strict.Violations) == 0 {
		t.Errorf("strict summary = %+v", strict)
	}
	// Plus the neighborhood: 3 class entities, and 5 edges (3 is_a, a subclass_of and a relation).
	if len(res.Entities) != 6 || len(res.Edges) != 6 || res.Edges[0].Target != "Rex" {
		t.Errorf("strict stored %d entities and %d edges: %+v", len(res.Entities), len(res.Edges), res.Edges)
	}
	tags = entityTags("strict")
	if len(tags) != 6 || !tags["Rex"].valid.Bool || !tags["Sam"].valid.Bool || !tags["Animal"].valid.Bool {
		t.Errorf("strict entities = %+v", tags)
	}
	dropped, _ := memory_ontology.Events(ctx, d, "u1", "keepers", graph.OntologyEventFilter{ArtifactID: res.ArtifactID, Outcome: "dropped"})
	if len(dropped) != 4 {
		t.Fatalf("%d dropped events, want 4", len(dropped))
	}
	for _, e := range dropped {
		if e.EntityID != 0 || e.EdgeID != 0 || e.Mode != "strict" {
			t.Errorf("dropped event = %+v", e)
		}
	}

	//------------------------------------------------------------------
	// refusals, and no grounding without the flag
	if _, err := memory_remember.Remember(ctx, d, &ontoLLM{}, "u1", "x", "", "text", relational.Now(),
		memory_core.WithOntologyGrounding([]string{"nope"}, "")); err == nil {
		t.Error("an unknown ontology must fail")
	}
	if _, err := memory_remember.Remember(ctx, d, &ontoLLM{}, "u1", "x", "", "text", relational.Now(),
		memory_core.WithOntologyGrounding(nil, "loose")); err == nil {
		t.Error("an unknown mode must fail")
	}
	l = &ontoLLM{}
	res, err = memory_remember.Remember(ctx, d, l, "u1", "plain", "", "Sam keeps Rex.", relational.Now())
	if err != nil || res.OntologyGrounding != nil || strings.Contains(l.systems[0], "<ontology>") || len(entityTags("plain")) != 5 || entityTags("plain")["Rex"].version.Valid {
		t.Errorf("without the flag: %v %v", err, res.OntologyGrounding)
	}
	runs, _, _ = relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{}, 1, 0)
	if st := runs[0].Stages[relational.RememberStageGroundGraph]; st.Status != relational.RememberSkipped {
		t.Errorf("ground_graph without the flag = %+v", st)
	}
}
