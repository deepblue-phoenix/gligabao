// Package ontology reads OWL ontologies written in RDF/XML and reduces them to what gligabao stores in its graph:
// a set of classes (nodes) and the relationships between them (edges).
//
// Mapping:
//   - every named class (owl:Class, rdfs:Class, or one only referenced) is a class node;
//   - rdfs:subClassOf between named classes is an edge "subclass_of" (child to parent);
//   - owl:equivalentClass and owl:disjointWith between named classes are the edges "equivalent_class" and
//     "disjoint_with" (symmetric, so stored once);
//   - every owl:ObjectProperty (or one of its subtypes, e.g. owl:TransitiveProperty) with an rdfs:domain and an
//     rdfs:range is an edge, named after the property, from each domain class to each range class. A domain or
//     range that is an owl:unionOf expression contributes each of its named members. owl:inverseOf is kept on the edge.
//
// Everything else (restrictions, datatype properties, individuals, properties without domain or range, other
// anonymous class expressions) is ignored: a document using only those still parses, it just yields no classes.
package ontology

import (
	"fmt"
	"io"
	"strings"
)

// Edge kinds.
const (
	KindSubClassOf      = "subclass_of"
	KindEquivalentClass = "equivalent_class"
	KindDisjointWith    = "disjoint_with"
	KindProperty        = "property"
)

const (
	iriSubClassOf      = nsRDFS + "subClassOf"
	iriEquivalentClass = nsOWL + "equivalentClass"
	iriDisjointWith    = nsOWL + "disjointWith"
)

// Class is a named class of the ontology.
type Class struct {
	IRI     string
	Name    string // local name of the IRI, e.g. "Market"
	Label   string // rdfs:label
	Comment string // rdfs:comment
	// Declared is true if the document itself defines the class (types it as owl:Class or rdfs:Class), and false if
	// the class is only mentioned, as an endpoint of a relationship. When an ontology is merged into a stored one,
	// only declared classes overwrite the stored class's name, label and comment; a class that is merely mentioned
	// is created if it is missing, and left untouched if it exists (see docs/ontologies.md).
	Declared bool
}

// Edge is a relationship between two classes. For KindProperty, Relation is the property's local name and IRI is
// the property's IRI; for the other kinds IRI is the RDFS/OWL predicate.
type Edge struct {
	SourceIRI, TargetIRI string
	Relation             string
	Kind                 string
	IRI                  string
	Label, Comment       string // of the property
	InverseOf            string // local name of the inverse property, if declared
}

// Ontology is the result of parsing a document.
type Ontology struct {
	IRI     string // the owl:Ontology IRI, if the document declares one
	Title   string // its rdfs:label (or dc:title)
	Comment string // its rdfs:comment
	Classes []Class
	Edges   []Edge
}

// Properties returns the number of distinct properties that became edges.
func (o *Ontology) Properties() int {
	seen := map[string]bool{}
	for _, e := range o.Edges {
		if e.Kind == KindProperty {
			seen[e.IRI] = true
		}
	}
	return len(seen)
}

// Parse reads an ontology in RDF/XML. It returns an error if the document is not well-formed RDF/XML or defines
// no class.
func Parse(r io.Reader) (*Ontology, error) {
	triples, err := parseRDFXML(r)
	if err != nil {
		return nil, err
	}
	o := interpret(triples)
	if len(o.Classes) == 0 {
		return nil, fmt.Errorf("the document defines no classes")
	}
	return o, nil
}

// LocalName returns the part of an IRI after the last '#' (or, without one, the last '/' or ':').
func LocalName(iri string) string {
	if i := strings.LastIndexAny(iri, "#/"); i >= 0 && i < len(iri)-1 {
		return iri[i+1:]
	}
	if i := strings.LastIndex(iri, ":"); i >= 0 && i < len(iri)-1 {
		return iri[i+1:]
	}
	return iri
}

//---------------------------------------------------------------------------------------------

var objectPropertyTypes = map[string]bool{
	nsOWL + "ObjectProperty":            true,
	nsOWL + "TransitiveProperty":        true,
	nsOWL + "SymmetricProperty":         true,
	nsOWL + "AsymmetricProperty":        true,
	nsOWL + "ReflexiveProperty":         true,
	nsOWL + "IrreflexiveProperty":       true,
	nsOWL + "InverseFunctionalProperty": true,
}

var titlePredicates = []string{nsRDFS + "label", "http://purl.org/dc/elements/1.1/title", "http://purl.org/dc/terms/title"}

type graphIndex struct {
	order  []string             // subjects in order of first appearance
	seen   map[string]bool      //
	values map[[2]string][]term // (subject, predicate) -> objects
}

func newIndex(ts []triple) *graphIndex {
	g := &graphIndex{seen: map[string]bool{}, values: map[[2]string][]term{}}
	for _, t := range ts {
		if !g.seen[t.s] {
			g.seen[t.s] = true
			g.order = append(g.order, t.s)
		}
		k := [2]string{t.s, t.p}
		g.values[k] = append(g.values[k], t.o)
	}
	return g
}

func (g *graphIndex) get(s, p string) []term { return g.values[[2]string{s, p}] }

func (g *graphIndex) hasType(s string, types func(string) bool) bool {
	for _, t := range g.get(s, rdfType) {
		if t.isIRI() && types(t.v) {
			return true
		}
	}
	return false
}

// literal returns the first non-empty literal of (s, one of preds).
func (g *graphIndex) literal(s string, preds ...string) string {
	for _, p := range preds {
		for _, t := range g.get(s, p) {
			if v := strings.TrimSpace(t.v); t.lit && v != "" {
				return v
			}
		}
	}
	return ""
}

// list returns the members of the RDF list starting at head.
func (g *graphIndex) list(head string) []term {
	var out []term
	for i := 0; head != rdfNil && i < 10000; i++ {
		first := g.get(head, rdfFirst)
		rest := g.get(head, rdfRest)
		if len(first) == 0 || len(rest) == 0 {
			break
		}
		out = append(out, first[0])
		head = rest[0].v
	}
	return out
}

// builtin reports whether iri belongs to the RDF, RDFS, OWL or XSD vocabularies (never a class of the ontology).
func builtin(iri string) bool {
	for _, ns := range []string{nsRDF, nsRDFS, nsOWL, nsXSD} {
		if strings.HasPrefix(iri, ns) {
			return true
		}
	}
	return false
}

// isNamed reports whether subject s is an IRI outside the standard vocabularies (not a blank node).
func isNamed(s string) bool { return !strings.HasPrefix(s, "_:") && !builtin(s) }

// members returns the named classes a class expression stands for: itself if it is named, its named members if
// it is an owl:unionOf, nothing otherwise.
func (g *graphIndex) members(t term, depth int) []string {
	switch {
	case t.isIRI():
		if builtin(t.v) {
			return nil
		}
		return []string{t.v}
	case t.isBlank() && depth < 8:
		var out []string
		for _, u := range g.get(t.v, nsOWL+"unionOf") {
			for _, m := range g.list(u.v) {
				out = append(out, g.members(m, depth+1)...)
			}
		}
		return out
	}
	return nil
}

func interpret(ts []triple) *Ontology {
	g := newIndex(ts)
	o := &Ontology{}

	// Classes, in order of appearance; classes only referenced by a relationship are added too.
	classIdx := map[string]int{}
	addClass := func(iri string, declared bool) {
		if _, ok := classIdx[iri]; ok {
			return
		}
		classIdx[iri] = len(o.Classes)
		o.Classes = append(o.Classes, Class{IRI: iri, Name: LocalName(iri), Declared: declared})
	}
	for _, s := range g.order {
		if isNamed(s) && g.hasType(s, func(t string) bool { return t == nsOWL+"Class" || t == nsRDFS+"Class" }) {
			addClass(s, true) // all declared classes are added before any merely mentioned one
		}
	}

	type edgeKey struct{ s, t, kind, iri string }
	edgeSeen := map[edgeKey]bool{}
	addEdge := func(e Edge) {
		k := edgeKey{e.SourceIRI, e.TargetIRI, e.Kind, e.IRI}
		if e.Kind == KindEquivalentClass || e.Kind == KindDisjointWith { // symmetric
			if e.SourceIRI > e.TargetIRI {
				k.s, k.t = k.t, k.s
			}
		}
		if edgeSeen[k] {
			return
		}
		edgeSeen[k] = true
		addClass(e.SourceIRI, false)
		addClass(e.TargetIRI, false)
		o.Edges = append(o.Edges, e)
	}

	// Relationships between classes.
	classPreds := []struct{ pred, kind string }{
		{iriSubClassOf, KindSubClassOf}, {iriEquivalentClass, KindEquivalentClass}, {iriDisjointWith, KindDisjointWith},
	}
	for _, s := range g.order {
		if !isNamed(s) {
			continue
		}
		for _, cp := range classPreds {
			for _, obj := range g.get(s, cp.pred) {
				if !obj.isIRI() { // restrictions and other anonymous expressions are ignored
					continue
				}
				for _, m := range g.members(obj, 0) {
					if cp.kind == KindSubClassOf && m == s {
						continue
					}
					addClass(s, false)
					addEdge(Edge{SourceIRI: s, TargetIRI: m, Relation: cp.kind, Kind: cp.kind, IRI: cp.pred})
				}
			}
		}
	}

	// Object properties become edges from their domain to their range.
	inverse := map[string]string{} // property IRI -> inverse property IRI, both directions
	for _, s := range g.order {
		for _, inv := range g.get(s, nsOWL+"inverseOf") {
			if inv.isIRI() {
				inverse[s], inverse[inv.v] = inv.v, s
			}
		}
	}
	for _, s := range g.order {
		if !isNamed(s) || !g.hasType(s, func(t string) bool { return objectPropertyTypes[t] }) {
			continue
		}
		var domain, rng []string
		for _, d := range g.get(s, nsRDFS+"domain") {
			domain = append(domain, g.members(d, 0)...)
		}
		for _, r := range g.get(s, nsRDFS+"range") {
			rng = append(rng, g.members(r, 0)...)
		}
		inv := ""
		if i, ok := inverse[s]; ok {
			inv = LocalName(i)
		}
		for _, d := range domain {
			for _, r := range rng {
				addEdge(Edge{
					SourceIRI: d, TargetIRI: r, Relation: LocalName(s), Kind: KindProperty, IRI: s,
					Label: g.literal(s, nsRDFS+"label"), Comment: g.literal(s, nsRDFS+"comment"), InverseOf: inv,
				})
			}
		}
	}

	for i := range o.Classes {
		c := &o.Classes[i]
		c.Label, c.Comment = g.literal(c.IRI, nsRDFS+"label"), g.literal(c.IRI, nsRDFS+"comment")
	}
	for _, s := range g.order {
		if g.hasType(s, func(t string) bool { return t == nsOWL+"Ontology" }) {
			if !strings.HasPrefix(s, "_:") {
				o.IRI = s
			}
			o.Title, o.Comment = g.literal(s, titlePredicates...), g.literal(s, nsRDFS+"comment")
			break
		}
	}
	return o
}
