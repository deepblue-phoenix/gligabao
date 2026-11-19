package ontospec

import "sort"

// RelationSubClassOf is the relation of the hierarchy edges of a Neighborhood (child class to parent class).
const RelationSubClassOf = "subclass_of"

// ClassNode is a class of the ontology that a Neighborhood brings into the graph.
type ClassNode struct {
	Name string
	Ref  Ref // the ontology that defines the class
}

// ClassEdge is an edge between two classes of a Neighborhood: a subClassOf link (Relation is RelationSubClassOf) or a relation of the
// ontology whose domain and range are both in the neighborhood.
type ClassEdge struct {
	Source, Relation, Target string
	Ref                      Ref // the ontology that defines the relation (for a subClassOf edge, the child class)
}

// Neighborhood is the part of the ontology around the classes that an extraction matched: the classes themselves, their ancestors, the
// hierarchy edges among them and the relations of the ontology that run between two of them.
type Neighborhood struct {
	Classes []ClassNode
	Edges   []ClassEdge
}

// Neighborhood returns the immediate ontology subgraph of the given classes (names or aliases; unknown ones are ignored): the classes and
// all their ancestors, one subClassOf edge from each class to each direct parent, and one edge per relation of the ontology for each
// pair (domain class, range class) that are both in the subgraph. A relation without a domain or without a range has no fixed ends and
// is left out. Nothing of the ontology outside the matched classes is returned. The result is sorted, so it is deterministic.
func (r *CompiledOntology) Neighborhood(classes []string) Neighborhood {
	inSet := foldSet(classes, r)
	names := sortedKeys(inSet)
	nodes := mapSlice(names, func(c string) ClassNode {
		return ClassNode{
			Name: c,
			Ref:  r.ClassRef(c),
		}
	})
	hierarchy := flatMap(names, func(c string) []ClassEdge {
		return mapSlice(r.parents(c), func(p string) ClassEdge {
			return ClassEdge{
				Source:   c,
				Relation: RelationSubClassOf,
				Target:   p,
				Ref:      r.ClassRef(c),
			}
		})
	})
	relations := flatMap(r.Relations(), func(name string) []ClassEdge {
		rel := r.relations[name]
		return flatMap(rel.domain, func(d string) []ClassEdge {
			return filterSlice(mapSlice(rel.rng, func(g string) ClassEdge {
				return ClassEdge{
					Source:   d,
					Relation: name,
					Target:   g,
					Ref:      r.RelationRef(name),
				}
			}), func(e ClassEdge) bool { return inSet[e.Source] && inSet[e.Target] })
		})
	})
	edges := append(hierarchy, relations...)
	sort.SliceStable(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Relation != b.Relation {
			return a.Relation < b.Relation
		}
		return a.Target < b.Target
	})
	return Neighborhood{
		Classes: nodes,
		Edges:   edges,
	}
}

// foldSet returns the set of the resolved classes and of all their ancestors.
func foldSet(classes []string,
	r *CompiledOntology) map[string]bool {
	out := map[string]bool{}
	for _, name := range classes {
		c, _, ok := r.Resolve(name)
		if !ok {
			continue
		}
		out[c] = true
		for _, a := range r.ancestors[c] {
			out[a] = true
		}
	}
	return out
}

// parents returns the direct parents of a class (canonical names, in the order of the spec).
func (r *CompiledOntology) parents(class string) []string {
	return mapSlice(r.Spec.Classes[class].SubClassOf, func(p string) string { return r.classByKey[Key(p)] })
}

func flatMap[T, R any](in []T,
	f func(T) []R) []R {
	var out []R
	for _, v := range in {
		out = append(out, f(v)...)
	}
	return out
}

func filterSlice[T any](in []T,
	keep func(T) bool) []T {
	var out []T
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}
