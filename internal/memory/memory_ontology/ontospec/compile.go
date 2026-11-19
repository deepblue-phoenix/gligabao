package ontospec

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// Ref says which stored ontology (and which version of it) defines a class or a relation.
type Ref struct {
	ID      int64  `json:"id,omitempty"`
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// Part is one ontology to compile, with the stored ontology it is.
type Part struct {
	Ref  Ref
	Spec Spec
}

// Registry is a compiled ontology: immutable, safe for concurrent use. It is the in-memory form the grounding and the extraction
// prompt work on; see Compile.
type Registry struct {
	// Spec is the merged spec the registry was compiled from.
	Spec Spec
	// Refs are the ontologies it was compiled from, in order.
	Refs []Ref

	classes     map[string]*classInfo    // by class name
	classByKey  map[string]string        // Key of a class name or alias -> class name
	aliasKeys   map[string]bool          // the keys that are aliases (not names)
	relations   map[string]*relationInfo // by relation name
	relByKey    map[string]string
	relAliasKey map[string]bool
	ancestors   map[string][]string // class -> its ancestors, nearest first
	descendants map[string][]string
	origin      map[string]Ref // "c:"+class or "r:"+relation -> the ontology that defines it
}

type classInfo struct {
	name  string
	props map[string]propInfo // own and inherited
}

type relationInfo struct {
	name     string
	domain   []string // class names
	rng      []string
	temporal bool
	props    map[string]propInfo
}

type propInfo struct {
	def PropertyDef
	re  *regexp.Regexp // nil without a pattern
}

// Merge unions several ontologies into one spec: the classes and relations of all, by name (compared by Key). A name that two of them
// define differently is an error that names both ontologies; defined identically it is the same. The returned origin says which
// ontology each class ("c:Name") and relation ("r:Name") came from (the first that defines it).
func Merge(parts []Part) (Spec, map[string]Ref, error) {
	merged := Spec{
		Classes:   map[string]ClassDef{},
		Relations: map[string]RelationDef{},
	}
	origin := map[string]Ref{}
	var problems []Problem
	for _, part := range parts {
		for _, name := range sortedKeys(part.Spec.Classes) {
			def := part.Spec.Classes[name]
			prev, at, ok := lookupKey(merged.Classes, name)
			switch {
			case !ok:
				merged.Classes[name] = def
				origin["c:"+name] = part.Ref
			case !reflect.DeepEqual(prev, def):
				problems = append(problems, conflict("class",
					name,
					origin["c:"+at],
					part.Ref))
			}
		}
		for _, name := range sortedKeys(part.Spec.Relations) {
			def := part.Spec.Relations[name]
			prev, at, ok := lookupKey(merged.Relations, name)
			switch {
			case !ok:
				merged.Relations[name] = def
				origin["r:"+name] = part.Ref
			case !reflect.DeepEqual(prev, def):
				problems = append(problems, conflict("relation",
					name,
					origin["r:"+at],
					part.Ref))
			}
		}
	}
	if len(problems) > 0 {
		return Spec{}, nil, &InvalidError{Problems: problems}
	}
	if len(parts) == 1 {
		merged.ID, merged.Version, merged.Description = parts[0].Spec.ID, parts[0].Spec.Version, parts[0].Spec.Description
	}
	return merged, origin, nil
}

func conflict(kind,
	name string,
	a,
	b Ref) Problem {
	return problem(kind+"s."+name,
		fmt.Sprintf("defined differently in the ontologies %q and %q",
			a.Name,
			b.Name))
}

// lookupKey finds the entry of m whose key is name (compared by Key), and returns it with its key.
func lookupKey[V any](m map[string]V,
	name string) (V, string, bool) {
	if v, ok := m[name]; ok {
		return v, name, true
	}
	for k, v := range m {
		if Key(k) == Key(name) {
			return v, k, true
		}
	}
	var zero V
	return zero, "", false
}

// Compile merges the parts, validates the result and builds the Registry. The error is an *InvalidError (wrapping ErrInvalid) with
// every problem found.
func Compile(parts ...Part) (*Registry, error) {
	spec, origin, err := Merge(parts)
	if err != nil {
		return nil, err
	}
	if problems := Validate(spec); len(problems) > 0 {
		return nil, &InvalidError{Problems: problems}
	}
	parents := parentMap(spec)
	reg := &Registry{
		Spec:        spec,
		Refs:        mapSlice(parts, func(p Part) Ref { return p.Ref }),
		classes:     map[string]*classInfo{},
		classByKey:  map[string]string{},
		aliasKeys:   map[string]bool{},
		relations:   map[string]*relationInfo{},
		relByKey:    map[string]string{},
		relAliasKey: map[string]bool{},
		ancestors:   map[string][]string{},
		descendants: map[string][]string{},
		origin:      origin,
	}
	for _, name := range sortedKeys(spec.Classes) {
		def := spec.Classes[name]
		reg.classByKey[Key(name)] = name
		for _, a := range def.Aliases {
			reg.classByKey[Key(a)] = name
			reg.aliasKeys[Key(a)] = true
		}
		anc := ancestorsOf(parents, name)
		reg.ancestors[name] = anc
		for _, a := range anc {
			reg.descendants[a] = append(reg.descendants[a], name)
		}
		// The properties of the ancestors first, the class's own last (it may only make a property required).
		props := map[string]propInfo{}
		for i := len(anc) - 1; i >= 0; i-- {
			props = mergeProps(props, spec.Classes[anc[i]].Properties)
		}
		reg.classes[name] = &classInfo{name: name, props: mergeProps(props, def.Properties)}
	}
	for _, list := range reg.descendants {
		sort.Strings(list)
	}
	for _, name := range sortedKeys(spec.Relations) {
		def := spec.Relations[name]
		reg.relByKey[Key(name)] = name
		for _, a := range def.Aliases {
			reg.relByKey[Key(a)] = name
			reg.relAliasKey[Key(a)] = true
		}
		reg.relations[name] = &relationInfo{
			name:     name,
			domain:   mapSlice(def.Domain, func(c string) string { return reg.classByKey[Key(c)] }),
			rng:      mapSlice(def.Range, func(c string) string { return reg.classByKey[Key(c)] }),
			temporal: def.Temporal,
			props:    mergeProps(nil, def.Properties),
		}
	}
	return reg, nil
}

// mergeProps returns base with defs added (a new map; the patterns are compiled, Validate has checked them).
func mergeProps(base map[string]propInfo,
	defs map[string]PropertyDef) map[string]propInfo {
	out := make(map[string]propInfo, len(base)+len(defs))
	for k, v := range base {
		out[k] = v
	}
	for k, d := range defs {
		info := propInfo{def: d}
		if d.Pattern != "" {
			info.re = regexp.MustCompile(d.Pattern)
		}
		out[k] = info
	}
	return out
}

//---------------------------------------------------------------------------------------------

// Resolve turns a class name as an extraction wrote it ("market participant", an alias) into the name of the class; viaAlias says
// it was an alias that matched, and ok that a class matched.
func (r *Registry) Resolve(name string) (class string, viaAlias,
	ok bool) {
	k := Key(name)
	class, ok = r.classByKey[k]
	return class, ok && r.aliasKeys[k], ok
}

// ResolveRelation is Resolve for relations.
func (r *Registry) ResolveRelation(name string) (relation string, viaAlias,
	ok bool) {
	k := Key(name)
	relation, ok = r.relByKey[k]
	return relation, ok && r.relAliasKey[k], ok
}

// IsSubClassOf says whether child is parent or one of its descendants. Both are class names or aliases.
func (r *Registry) IsSubClassOf(child,
	parent string) bool {
	c, _, okC := r.Resolve(child)
	p, _, okP := r.Resolve(parent)
	if !okC || !okP {
		return false
	}
	return c == p || contains(r.ancestors[c], p)
}

// Ancestors returns the ancestors of a class, nearest first (nil for an unknown class).
func (r *Registry) Ancestors(class string) []string {
	c, _, _ := r.Resolve(class)
	return append([]string(nil), r.ancestors[c]...)
}

// Descendants returns the descendants of a class, sorted (nil for an unknown class).
func (r *Registry) Descendants(class string) []string {
	c, _, _ := r.Resolve(class)
	return append([]string(nil), r.descendants[c]...)
}

// Expand returns the class and its descendants: what a query for the class stands for.
func (r *Registry) Expand(class string) []string {
	c, _, ok := r.Resolve(class)
	if !ok {
		return nil
	}
	return append([]string{c}, r.descendants[c]...)
}

// Classes returns the class names, sorted.
func (r *Registry) Classes() []string { return sortedKeys(r.classes) }

// Relations returns the relation names, sorted.
func (r *Registry) Relations() []string { return sortedKeys(r.relations) }

// ClassRef returns the ontology that defines a class, by its name.
func (r *Registry) ClassRef(class string) Ref { return r.origin["c:"+class] }

// RelationRef returns the ontology that defines a relation, by its name.
func (r *Registry) RelationRef(relation string) Ref { return r.origin["r:"+relation] }

//---------------------------------------------------------------------------------------------
// The extraction schema and the prompt

// ExtractionJSON is the JSON Schema (draft 2020-12) of what the extraction must return under the ontology: a branch per class for the
// entities (the type is a constant, the properties are those of the class and its ancestors, no others) and a branch per relation for
// the edges. The domain and range of a relation cannot be written in JSON Schema; they are in its description, and Ground checks them.
func (r *Registry) ExtractionJSON() map[string]any {
	entityBranches := mapSlice(r.Classes(), func(name string) any {
		return map[string]any{
			"type":     "object",
			"required": []string{"name", "type"},
			"properties": map[string]any{
				"name":        map[string]any{"type": "string"},
				"type":        map[string]any{"const": name},
				"description": map[string]any{"type": "string"},
				"properties":  propsSchema(r.classes[name].props),
			},
			"additionalProperties": false,
		}
	})
	edgeBranches := mapSlice(r.Relations(), func(name string) any {
		rel := r.relations[name]
		props := map[string]any{
			"source":      map[string]any{"type": "string"},
			"target":      map[string]any{"type": "string"},
			"relation":    map[string]any{"const": name},
			"description": map[string]any{"type": "string"},
			"properties":  propsSchema(rel.props),
		}
		if rel.temporal {
			props["valid_at"] = map[string]any{"type": []string{"string", "null"}, "format": "date-time"}
			props["invalid_at"] = map[string]any{"type": []string{"string", "null"}, "format": "date-time"}
		}
		return map[string]any{
			"type":                 "object",
			"description":          relationEnds(rel),
			"required":             []string{"source", "target", "relation"},
			"properties":           props,
			"additionalProperties": false,
		}
	})
	return map[string]any{
		"$schema":  "https://json-schema.org/draft/2020-12/schema",
		"type":     "object",
		"required": []string{"entities", "edges"},
		"properties": map[string]any{
			"entities": map[string]any{"type": "array", "items": map[string]any{"anyOf": entityBranches}},
			"edges":    map[string]any{"type": "array", "items": map[string]any{"anyOf": edgeBranches}},
		},
		"additionalProperties": false,
	}
}

func relationEnds(rel *relationInfo) string {
	end := func(l []string) string {
		if len(l) == 0 {
			return "any class"
		}
		return strings.Join(l, " or ")
	}
	return "source: " + end(rel.domain) + "; target: " + end(rel.rng)
}

func propsSchema(props map[string]propInfo) map[string]any {
	names := sortedKeys(props)
	required := []string{}
	properties := map[string]any{}
	for _, n := range names {
		d := props[n].def
		if d.Required {
			required = append(required, n)
		}
		properties[n] = propSchema(d)
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func propSchema(d PropertyDef) map[string]any {
	out := map[string]any{}
	switch d.Type {
	case TypeDate:
		out["type"] = "string"
		out["description"] = "an ISO 8601 date or date-time"
	default:
		out["type"] = d.Type
	}
	if d.Description != "" {
		out["description"] = d.Description
	}
	if len(d.Enum) > 0 {
		out["enum"] = d.Enum
	}
	if d.Pattern != "" {
		out["pattern"] = d.Pattern
	}
	return out
}

// Prompt describes the ontology in words, for the extraction prompt: the classes (with parents, descriptions and properties) and
// the relations (domain, range, properties).
func (r *Registry) Prompt() string {
	var b strings.Builder
	b.WriteString("Classes (the allowed values of an entity's \"type\"):\n")
	for _, name := range r.Classes() {
		def := r.Spec.Classes[name]
		b.WriteString("- " + name)
		if len(def.SubClassOf) > 0 {
			b.WriteString(" (a kind of " + strings.Join(def.SubClassOf, ", ") + ")")
		}
		if def.Description != "" {
			b.WriteString(": " + def.Description)
		}
		b.WriteString("\n")
		b.WriteString(propLines(r.classes[name].props))
	}
	b.WriteString("\nRelations (the allowed values of an edge's \"relation\"):\n")
	for _, name := range r.Relations() {
		rel := r.relations[name]
		def := r.Spec.Relations[name]
		b.WriteString("- " + name + " (" + relationEnds(rel) + ")")
		if def.Description != "" {
			b.WriteString(": " + def.Description)
		}
		b.WriteString("\n")
		b.WriteString(propLines(rel.props))
	}
	return b.String()
}

func propLines(props map[string]propInfo) string {
	var b strings.Builder
	for _, n := range sortedKeys(props) {
		d := props[n].def
		b.WriteString("    property " + n + ": " + d.Type)
		if d.Required {
			b.WriteString(", required")
		}
		if len(d.Enum) > 0 {
			b.WriteString(", one of " + strings.Join(d.Enum, "|"))
		}
		if d.Pattern != "" {
			b.WriteString(", matching " + d.Pattern)
		}
		if d.Description != "" {
			b.WriteString(" (" + d.Description + ")")
		}
		b.WriteString("\n")
	}
	return b.String()
}
