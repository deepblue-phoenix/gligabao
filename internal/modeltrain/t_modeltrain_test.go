package modeltrain

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

func cls(artifact int64,
	names ...string) []graph.ArtifactClass {
	out := []graph.ArtifactClass{}
	for _, n := range names {
		out = append(out, graph.ArtifactClass{
			ArtifactID: artifact,
			Class:      n,
		})
	}
	return out
}

func agraph(id int64,
	classes []graph.ArtifactClass) memory_core.ArtifactGraph {
	return memory_core.ArtifactGraph{
		ArtifactID: id,
		Entities: []graph.EvidenceEntity{
			{ID: id*10 + 1, Name: "a", Type: "person"},
			{ID: id*10 + 2, Name: "b", Type: "org"},
		},
		Edges: []graph.EvidenceEdge{
			{SourceID: id*10 + 1, TargetID: id*10 + 2},
			{SourceID: id*10 + 2, TargetID: id*10 + 1},
			{SourceID: id*10 + 1, TargetID: id*10 + 1},
			{SourceID: id*10 + 1, TargetID: 999},
		},
		Classes: classes,
	}
}

func dataset(graphs ...memory_core.ArtifactGraph) memory_core.KnowledgeDataset {
	artifacts := []graph.Artifact{}
	for _, g := range graphs {
		artifacts = append(artifacts, graph.Artifact{ID: g.ArtifactID})
	}
	return memory_core.KnowledgeDataset{
		Artifacts: artifacts,
		Graphs:    graphs,
	}
}

func TestPickClass(t *testing.T) {
	got := PickClass(cls(1, "Staff Room", "legal", "legal", "x"), nil)
	if got != "legal" {
		t.Fatalf("most given: %q", got)
	}
	if got := PickClass(cls(1, "b", "a"), nil); got != "b" {
		t.Fatalf("earliest on a tie: %q", got)
	}
	if got := PickClass(cls(1, "b", "a"), []string{"a"}); got != "a" {
		t.Fatalf("allowed: %q", got)
	}
	if got := PickClass(cls(1, "b"), []string{"a"}); got != "" {
		t.Fatalf("none allowed: %q", got)
	}
}

func TestToGCNIIGraph(t *testing.T) {
	g, ok := ToGCNIIGraph(agraph(1, nil), "c")
	if !ok || g.N != 2 || g.Class != "c" || !reflect.DeepEqual(g.Edges, [][2]int{{0, 1}}) {
		t.Fatalf("%+v %v", g, ok)
	}
	if g.Nodes[1].Type != "org" {
		t.Fatalf("nodes: %+v", g.Nodes)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, ok := ToGCNIIGraph(memory_core.ArtifactGraph{}, "c"); ok {
		t.Fatal("a graph without entities has no conversion")
	}
}

func TestDefaultHookAndSets(t *testing.T) {
	ds := dataset(
		agraph(1, cls(1, "legal")),
		agraph(2, cls(2, "legal")),
		agraph(3, cls(3, "legal")),
		agraph(4, cls(4, "legal")),
		agraph(5, cls(5, "medical")),
		agraph(6, cls(6, "medical")),
		agraph(7, cls(7, "rare")),
		agraph(8, nil))
	opts := Options{
		MinGraphsPerClass:  2,
		ValidationFraction: 0.5,
		Seed:               7,
	}
	td, err := DefaultHook(opts)(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory_core.ValidateTrainingDataset(ds, td); err != nil {
		t.Fatal(err)
	}
	if len(td.Graphs) != 6 || len(td.Artifacts) != 6 {
		t.Fatalf("kept %d graphs, %d artifacts", len(td.Graphs), len(td.Artifacts))
	}
	sets, err := SetsOf(td, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets.Train) != 3 || len(sets.Validation) != 3 || !reflect.DeepEqual(sets.Classes, []string{"legal", "medical"}) {
		t.Fatalf("%d train, %d validation, %v", len(sets.Train), len(sets.Validation), sets.Classes)
	}
	again, _ := SetsOf(td, opts)
	if !reflect.DeepEqual(sets.ArtifactsValidation, again.ArtifactsValidation) {
		t.Fatal("the split is not deterministic")
	}
	seen := map[int64]bool{}
	for _, id := range append(append([]int64{}, sets.ArtifactsTrain...), sets.ArtifactsValidation...) {
		if seen[id] {
			t.Fatalf("artifact %d is in both sets", id)
		}
		seen[id] = true
	}
}

func TestSplitKeepsOneTrainingGraphPerClass(t *testing.T) {
	items := []Item{
		{ArtifactID: 1},
		{ArtifactID: 2},
	}
	items[0].Graph.Class, items[1].Graph.Class = "a", "b"
	train, val := Split(items,
		0.9,
		1)
	if len(train) != 2 || len(val) != 0 {
		t.Fatalf("%d %d", len(train), len(val))
	}
}

func TestSetsOfNoGraphs(t *testing.T) {
	td, _ := DefaultHook(Options{})(context.Background(), dataset(agraph(1, nil)))
	if _, err := SetsOf(td, Options{}); !errors.Is(err, ErrNoGraphs) {
		t.Fatalf("%v", err)
	}
}

func TestOptionsValidate(t *testing.T) {
	for _, o := range []Options{
		{ValidationFraction: 1},
		{ValidationFraction: -0.1},
		{MinGraphsPerClass: -1},
		{Classes: []string{"!!"}},
	} {
		if err := o.Validate(); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("%+v: %v", o, err)
		}
	}
}
