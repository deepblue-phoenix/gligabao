package modeltrain

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

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
	got := PickClass(cls(1, "Staff Room", "legal", "legal", "x"), nil, "")
	if got != "legal" {
		t.Fatalf("most given: %q", got)
	}
	if got := PickClass(cls(1, "b", "a"), nil, PickMajority); got != "b" {
		t.Fatalf("earliest on a tie: %q", got)
	}
	if got := PickClass(cls(1, "b", "a"), []string{"a"}, ""); got != "a" {
		t.Fatalf("allowed: %q", got)
	}
	if got := PickClass(cls(1, "b"), []string{"a"}, ""); got != "" {
		t.Fatalf("none allowed: %q", got)
	}
}

func TestPickClassLatest(t *testing.T) {
	records := []graph.ArtifactClass{
		{Class: "old", InferenceID: 1},
		{Class: "old", InferenceID: 1},
		{Class: "new_first", InferenceID: 5},
		{Class: "new_second", InferenceID: 5},
	}
	if got := PickClass(records, nil, PickLatest); got != "new_first" {
		t.Fatalf("first class of the latest run: %q", got)
	}
	if got := PickClass(records, nil, PickMajority); got != "old" {
		t.Fatalf("majority: %q", got)
	}
	if got := PickClass(nil, nil, PickLatest); got != "" {
		t.Fatalf("no records: %q", got)
	}
}

func TestToGCNIIGraph(t *testing.T) {
	g, ok := ToGCNIIGraph(agraph(1, nil), "c")
	// the fact to entity 999 adds a node named after its endpoint; the self loop and the repeat are dropped
	if !ok || g.N != 3 || g.Class != "c" || !reflect.DeepEqual(g.Edges, [][2]int{{0, 1}, {0, 2}}) {
		t.Fatalf("%+v %v", g, ok)
	}
	if g.Nodes[1].Type != "org" || g.Nodes[2].Type != "" {
		t.Fatalf("nodes: %+v", g.Nodes)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, ok := ToGCNIIGraph(memory_core.ArtifactGraph{}, "c"); ok {
		t.Fatal("a graph without nodes has no conversion")
	}
	only, ok := ToGCNIIGraph(memory_core.ArtifactGraph{
		Edges: []graph.EvidenceEdge{{SourceID: 1, TargetID: 2, Source: "x", Target: "y"}},
	}, "c")
	if !ok || only.N != 2 || only.Nodes[0].Name != "x" {
		t.Fatalf("endpoints become nodes: %+v", only)
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

func TestSetsOfNeedsTwoClasses(t *testing.T) {
	ds := dataset(agraph(1, cls(1, "legal")),
		agraph(2, cls(2, "legal")))
	td, _ := DefaultHook(Options{})(context.Background(), ds)
	if _, err := SetsOf(td, Options{}); !errors.Is(err, ErrTooFewClasses) {
		t.Fatalf("%v", err)
	}
}

func TestMinEdges(t *testing.T) {
	edgeless := agraph(1, cls(1, "a"))
	edgeless.Edges = nil
	ds := dataset(edgeless,
		agraph(2, cls(2, "a")),
		agraph(3, cls(3, "b")))
	td, _ := DefaultHook(Options{})(context.Background(), ds)
	if len(td.Graphs) != 2 {
		t.Fatalf("a graph without an edge is left out: %d", len(td.Graphs))
	}
	// agraph has two distinct edges after the cleanup of the conversion
	td, _ = DefaultHook(Options{MinEdges: 3})(context.Background(), ds)
	if len(td.Graphs) != 0 {
		t.Fatalf("min_edges 3: %d", len(td.Graphs))
	}
}

func TestTopClassesAndOther(t *testing.T) {
	ds := dataset(
		agraph(1, cls(1, "a")), agraph(2, cls(2, "a")), agraph(3, cls(3, "a")),
		agraph(4, cls(4, "b")), agraph(5, cls(5, "b")),
		agraph(6, cls(6, "c")),
		agraph(7, cls(7, "d")))
	with, _ := DefaultHook(Options{TopClasses: 2, OtherClass: true})(context.Background(), ds)
	if got := TopOf(with.Classes, 0); !reflect.DeepEqual(got, []string{"a", "b", OtherClass}) {
		t.Fatalf("with other: %v", got)
	}
	if len(with.Graphs) != 7 || with.Classes[7] != OtherClass {
		t.Fatalf("with other: %d graphs, %v", len(with.Graphs), with.Classes)
	}
	without, _ := DefaultHook(Options{TopClasses: 2})(context.Background(), ds)
	if len(without.Graphs) != 5 {
		t.Fatalf("without other the tail is left out: %d", len(without.Graphs))
	}
}

func TestRunHistory(t *testing.T) {
	dir := t.TempDir()
	td, _ := DefaultHook(Options{})(context.Background(), dataset(
		agraph(1, cls(1, "a")), agraph(2, cls(2, "b"))))
	opts := Options{TopClasses: 3}
	sets, err := SetsOf(td, opts)
	if err != nil {
		t.Fatal(err)
	}
	first := NewRun("u/1", "my set", time.Unix(100, 0), opts, TrainParams{Epochs: 5}, sets)
	second := NewRun("u/1", "my set", time.Unix(200, 0), opts, TrainParams{}, sets)
	for _, run := range []Run{first, second} {
		if err := SaveRun(dir, run); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveRun(dir, first); err == nil {
		t.Fatal("a run is never overwritten")
	}
	runs, err := ListRuns(dir, "u/1", "my set")
	if err != nil || len(runs) != 2 || runs[0].ID != second.ID || runs[1].Params.Epochs != 5 || runs[1].Options.TopClasses != 3 {
		t.Fatalf("%+v %v", runs, err)
	}
	if runs, err := ListRuns(dir, "nobody", "x"); err != nil || len(runs) != 0 {
		t.Fatalf("%+v %v", runs, err)
	}
}

func TestOptionsValidate(t *testing.T) {
	for _, o := range []Options{
		{ValidationFraction: 1},
		{ValidationFraction: -0.1},
		{MinGraphsPerClass: -1},
		{Classes: []string{"!!"}},
		{TopClasses: -1},
		{TopClasses: MaxTopClasses + 1},
		{MinEdges: -1},
		{ClassPick: "random"},
	} {
		if err := o.Validate(); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("%+v: %v", o, err)
		}
	}
}
