// Package ontospec is the ontology DSL of gligabao: a declarative typed property graph (classes, relations, properties) written in
// YAML or JSON, not OWL. It holds everything that is pure, with no database and no LLM:
//
//   - the Spec and its Parse and Validate (the ontology checked against itself);
//   - Compile, which turns a Spec (or several, merged) into a Registry: an in-memory structure with the class hierarchy, alias
//     lookup, property validators and the JSON Schema of the LLM's extraction output;
//   - Ground, which checks the nodes and edges an extraction made against a Registry (see ground.go), and the summaries of the
//     result (see summary.go).
//
// The stored ontologies, their versions and the hot swap of the registries are in the parent package, memory_ontology.
package ontospec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Formats of a document.
const (
	FormatYAML = "yaml"
	FormatJSON = "json"
)

// Property types.
const (
	TypeString  = "string"
	TypeNumber  = "number"
	TypeInteger = "integer"
	TypeBoolean = "boolean"
	TypeDate    = "date" // an ISO 8601 date (2025-01-31) or date-time (RFC 3339)
)

// PropertyTypes are the valid types of a property.
var PropertyTypes = []string{TypeString, TypeNumber, TypeInteger, TypeBoolean, TypeDate}

// ErrInvalid is wrapped by the errors that say a document or a spec is not valid.
var ErrInvalid = errors.New("invalid ontology")

// Names is a list of names that may be written as one name: `domain: Trade` and `domain: [Asset, Order]` are both valid.
type Names []string

// PropertyDef is a property of a class or of a relation (an edge).
type PropertyDef struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`    // string only: the allowed values
	Pattern     string   `json:"pattern,omitempty"` // string only: a regular expression the value must match
	Required    bool     `json:"required,omitempty"`
}

// ClassDef is a class of the ontology (a type of node).
type ClassDef struct {
	SubClassOf  Names                  `json:"subClassOf,omitempty"`
	Description string                 `json:"description,omitempty"`
	Properties  map[string]PropertyDef `json:"properties,omitempty"`
	Aliases     []string               `json:"aliases,omitempty"` // other names that resolve to this class
}

// RelationDef is a relation of the ontology (a type of edge) between nodes of the classes of its domain and its range. An empty
// domain or range puts no limit on that end.
type RelationDef struct {
	Description string                 `json:"description,omitempty"`
	Domain      Names                  `json:"domain,omitempty"`
	Range       Names                  `json:"range,omitempty"`
	Temporal    bool                   `json:"temporal,omitempty"` // the edge carries valid_at / invalid_at
	Properties  map[string]PropertyDef `json:"properties,omitempty"`
	Aliases     []string               `json:"aliases,omitempty"`
}

// Spec is an ontology.
type Spec struct {
	// ID names the ontology (informational; the name it is stored under is what identifies it).
	ID string `json:"id,omitempty"`
	// Version is a label the author gives the document. It is not the ontology version the server stamps on the nodes and edges (that
	// is an integer, bumped by every save).
	Version     string                 `json:"version,omitempty"`
	Description string                 `json:"description,omitempty"`
	Classes     map[string]ClassDef    `json:"classes"`
	Relations   map[string]RelationDef `json:"relations,omitempty"`
}

// Problem is one thing wrong with a spec; Path locates it, e.g. "classes.Buyer.subClassOf".
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func problem(path,
	message string) Problem {
	return Problem{
		Path:    path,
		Message: message,
	}
}

func (p Problem) String() string { return p.Path + ": " + p.Message }

// InvalidError is the error of a spec with problems; it wraps ErrInvalid.
type InvalidError struct {
	Problems []Problem
}

func (e *InvalidError) Error() string {
	return ErrInvalid.Error() + ": " + strings.Join(mapSlice(e.Problems, Problem.String), "; ")
}

func (e *InvalidError) Unwrap() error { return ErrInvalid }

// UnmarshalJSON accepts a name or a list of names.
func (n *Names) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*n = Names{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("must be a name or a list of names")
	}
	*n = many
	return nil
}

//---------------------------------------------------------------------------------------------

// Key is the form of a name that names are compared in: lowercase letters and digits only, so that "MarketParticipant",
// "market_participant" and "market participant" are the same name (the extraction normalizes types and relations to lowercase
// snake_case, and the ontology is written in whatever case suits it).
func Key(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	},
		name)
}

// Parse reads a spec written in format ("yaml" or "json"; YAML is a superset of JSON, so either reads both). Unknown fields are
// an error. It checks the shape of the document, not its meaning: see Validate.
func Parse(format string,
	data []byte) (Spec, error) {
	var spec Spec
	var generic any
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatYAML, "yml", "":
		if err := yaml.Unmarshal(data, &generic); err != nil {
			return spec, fmt.Errorf("%w: yaml: %v", ErrInvalid, err)
		}
	case FormatJSON:
		if err := json.Unmarshal(data, &generic); err != nil {
			return spec, fmt.Errorf("%w: json: %v", ErrInvalid, err)
		}
	default:
		return spec, fmt.Errorf("%w: unsupported format %q (use yaml or json)", ErrInvalid, format)
	}
	asJSON, err := json.Marshal(normalizeGeneric(generic))
	if err != nil {
		return spec, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	dec := json.NewDecoder(bytes.NewReader(asJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return spec, nil
}

// normalizeGeneric makes what yaml.v3 decoded JSON-encodable: a `version: 1` is a number, but Spec.Version is a string.
func normalizeGeneric(v any) any {
	root, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if ver, ok := root["version"]; ok {
		if _, isString := ver.(string); !isString && ver != nil {
			root["version"] = fmt.Sprint(ver)
		}
	}
	return root
}

// DetectFormat guesses the format of a document from its first non-blank byte: "json" for "{", else "yaml".
func DetectFormat(data []byte) string {
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '{' {
		return FormatJSON
	}
	return FormatYAML
}

//---------------------------------------------------------------------------------------------

// Validate checks the ontology against itself: unknown parents, parent cycles, unknown domain and range classes, unknown property
// types, enums and patterns, name and alias collisions, and properties a subclass redefines differently. The problems are sorted
// by path; none means the spec is valid.
func Validate(s Spec) []Problem {
	problems := concat(
		validateShape(s),
		validateParents(s),
		validateRelationEnds(s),
		validateProperties(s),
		validateCollisions(s))
	if len(problems) == 0 {
		problems = validateInheritedProperties(s)
	}
	sort.SliceStable(problems, func(i, j int) bool { return problems[i].Path < problems[j].Path })
	return problems
}

func validateShape(s Spec) []Problem {
	var out []Problem
	if len(s.Classes) == 0 {
		out = append(out, problem("classes", "the ontology defines no classes"))
	}
	for _, name := range sortedKeys(s.Classes) {
		if Key(name) == "" {
			out = append(out, problem("classes."+name, "the name needs letters or digits"))
		}
	}
	for _, name := range sortedKeys(s.Relations) {
		if Key(name) == "" {
			out = append(out, problem("relations."+name, "the name needs letters or digits"))
		}
	}
	return out
}

func validateParents(s Spec) []Problem {
	var out []Problem
	known := classKeys(s)
	for _, name := range sortedKeys(s.Classes) {
		for _, p := range s.Classes[name].SubClassOf {
			switch {
			case known[Key(p)] == "":
				out = append(out, problem("classes."+name+".subClassOf", fmt.Sprintf("unknown class %q", p)))
			case known[Key(p)] == name:
				out = append(out, problem("classes."+name+".subClassOf", "a class cannot be its own parent"))
			}
		}
	}
	// A cycle: a class that is its own ancestor. Reported once per class on a cycle.
	parents := parentMap(s)
	for _, name := range sortedKeys(s.Classes) {
		if reaches(parents, name, name) {
			out = append(out, problem("classes."+name+".subClassOf", "subClassOf cycle through "+name))
		}
	}
	return out
}

func validateRelationEnds(s Spec) []Problem {
	var out []Problem
	known := classKeys(s)
	for _, name := range sortedKeys(s.Relations) {
		r := s.Relations[name]
		for _, end := range []struct {
			field string
			names Names
		}{{"domain", r.Domain}, {"range", r.Range}} {
			for _, c := range end.names {
				if known[Key(c)] == "" {
					out = append(out, problem("relations."+name+"."+end.field, fmt.Sprintf("unknown class %q", c)))
				}
			}
		}
	}
	return out
}

func validateProperties(s Spec) []Problem {
	var out []Problem
	for _, name := range sortedKeys(s.Classes) {
		out = append(out, validatePropertyDefs("classes."+name, s.Classes[name].Properties)...)
	}
	for _, name := range sortedKeys(s.Relations) {
		out = append(out, validatePropertyDefs("relations."+name, s.Relations[name].Properties)...)
	}
	return out
}

func validatePropertyDefs(path string,
	props map[string]PropertyDef) []Problem {
	var out []Problem
	for _, pname := range sortedKeys(props) {
		p := props[pname]
		at := path + ".properties." + pname
		if !contains(PropertyTypes, p.Type) {
			out = append(out, problem(at+".type", fmt.Sprintf("unknown type %q (use one of %s)", p.Type, strings.Join(PropertyTypes, ", "))))
			continue
		}
		if (len(p.Enum) > 0 || p.Pattern != "") && p.Type != TypeString {
			out = append(out, problem(at, "enum and pattern are only for the type string"))
		}
		if p.Pattern != "" {
			if _, err := regexp.Compile(p.Pattern); err != nil {
				out = append(out, problem(at+".pattern", "not a valid regular expression: "+err.Error()))
			}
		}
		for _, v := range p.Enum {
			if v == "" {
				out = append(out, problem(at+".enum", "an enum value cannot be empty"))
			}
		}
	}
	return out
}

// validateCollisions finds names and aliases that mean two things: within the classes, and within the relations (the two are
// separate namespaces).
func validateCollisions(s Spec) []Problem {
	classNames := mapKeysToSlice(s.Classes, func(n string, c ClassDef) names { return names{n, c.Aliases} })
	relNames := mapKeysToSlice(s.Relations, func(n string, r RelationDef) names { return names{n, r.Aliases} })
	return concat(collisions("classes", classNames), collisions("relations", relNames))
}

type names struct {
	name    string
	aliases []string
}

func collisions(section string,
	all []names) []Problem {
	owner := map[string]string{} // key -> the name that first used it
	var out []Problem
	for _, n := range all {
		for _, label := range append([]string{n.name}, n.aliases...) {
			k := Key(label)
			if k == "" {
				if label != n.name {
					out = append(out, problem(section+"."+n.name+".aliases", "an alias needs letters or digits"))
				}
				continue
			}
			if first, ok := owner[k]; ok && first != n.name {
				out = append(out, problem(section+"."+n.name, fmt.Sprintf("the name or alias %q is already used by %q", label, first)))
				continue
			}
			owner[k] = n.name
		}
	}
	return out
}

// validateInheritedProperties finds a property a subclass defines again with another type, enum or pattern than an ancestor's.
// Only called on a spec whose hierarchy is sound.
func validateInheritedProperties(s Spec) []Problem {
	var out []Problem
	parents := parentMap(s)
	for _, name := range sortedKeys(s.Classes) {
		for _, anc := range ancestorsOf(parents, name) {
			for pname, mine := range s.Classes[name].Properties {
				if theirs, ok := s.Classes[anc].Properties[pname]; ok && !sameShape(mine, theirs) {
					out = append(out, problem("classes."+name+".properties."+pname,
						fmt.Sprintf("redefines the property of %q with another type, enum or pattern", anc)))
				}
			}
		}
	}
	return out
}

// sameShape says two definitions accept the same values (the description and required may differ: a subclass can make a property
// required).
func sameShape(a,
	b PropertyDef) bool {
	return a.Type == b.Type && a.Pattern == b.Pattern && strings.Join(a.Enum, "\x00") == strings.Join(b.Enum, "\x00")
}

//---------------------------------------------------------------------------------------------
// helpers on the hierarchy of a Spec

// classKeys maps the Key of every class name to the class name.
func classKeys(s Spec) map[string]string {
	out := make(map[string]string, len(s.Classes))
	for name := range s.Classes {
		out[Key(name)] = name
	}
	return out
}

// parentMap maps a class to its parents, as class names (unknown parents are left out).
func parentMap(s Spec) map[string][]string {
	known := classKeys(s)
	out := make(map[string][]string, len(s.Classes))
	for name, c := range s.Classes {
		for _, p := range c.SubClassOf {
			if canon := known[Key(p)]; canon != "" {
				out[name] = append(out[name], canon)
			}
		}
	}
	return out
}

// reaches says whether to is an ancestor of from (following parents, not counting from itself unless it is on a cycle).
func reaches(parents map[string][]string,
	from,
	to string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(c string) bool {
		for _, p := range parents[c] {
			if p == to {
				return true
			}
			if !seen[p] {
				seen[p] = true
				if walk(p) {
					return true
				}
			}
		}
		return false
	}
	return walk(from)
}

// ancestorsOf returns every ancestor of class c (not c), nearest first, without repeats.
func ancestorsOf(parents map[string][]string,
	c string) []string {
	var out []string
	seen := map[string]bool{c: true}
	queue := append([]string(nil), parents[c]...)
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		queue = append(queue, parents[p]...)
	}
	return out
}

//---------------------------------------------------------------------------------------------
// small generic helpers

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mapKeysToSlice[V, R any](m map[string]V,
	f func(string, V) R) []R {
	keys := sortedKeys(m)
	out := make([]R, len(keys))
	for i, k := range keys {
		out[i] = f(k, m[k])
	}
	return out
}

func mapSlice[T, R any](in []T,
	f func(T) R) []R {
	out := make([]R, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

func concat[T any](lists ...[]T) []T {
	var out []T
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

func contains(list []string,
	s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
