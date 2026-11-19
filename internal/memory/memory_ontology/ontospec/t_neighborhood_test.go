package ontospec

import (
	"reflect"
	"testing"
)

func TestNeighborhood(t *testing.T) {
	reg := mustCompile(t, zoo)

	got := reg.Neighborhood([]string{"puppy", "Keeper", "nonsense"})
	names := mapSlice(got.Classes, func(c ClassNode) string { return c.Name })
	if want := []string{"Animal", "Dog", "Keeper", "Puppy"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("classes = %v, want %v", names, want)
	}
	want := []ClassEdge{
		{Source: "Dog", Relation: "chases", Target: "Animal"},
		{Source: "Dog", Relation: RelationSubClassOf, Target: "Animal"},
		{Source: "Keeper", Relation: "keeps", Target: "Dog"},
		{Source: "Puppy", Relation: RelationSubClassOf, Target: "Dog"},
	}
	// The refs are compared apart: the compiled zoo has one ontology.
	plain := mapSlice(got.Edges, func(e ClassEdge) ClassEdge {
		e.Ref = Ref{}
		return e
	})
	if !reflect.DeepEqual(plain, want) {
		t.Fatalf("edges = %v, want %v", got.Edges, want)
	}

	// Only a matched class and its ancestors come in: Keeper's relation to Dog needs Dog in the subgraph.
	alone := reg.Neighborhood([]string{"Keeper"})
	if len(alone.Classes) != 1 || len(alone.Edges) != 0 {
		t.Fatalf("Keeper alone = %+v", alone)
	}
	if n := reg.Neighborhood(nil); len(n.Classes) != 0 || len(n.Edges) != 0 {
		t.Fatalf("nothing matched = %+v", n)
	}
}
