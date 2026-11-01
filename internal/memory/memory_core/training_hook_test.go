package memory_core

import (
	"context"
	"errors"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

func TestValidateTrainingDataset(t *testing.T) {
	in := KnowledgeDataset{
		Artifacts: []graph.Artifact{{ID: 1}, {ID: 2}, {ID: 3}},
		Graphs:    []ArtifactGraph{{ArtifactID: 1}, {ArtifactID: 2}},
	}
	sub := func(artifacts []int64, graphs []int64, classes map[int64]string) TrainingDataset {
		td := TrainingDataset{Classes: classes}
		for _, id := range artifacts {
			td.Artifacts = append(td.Artifacts, graph.Artifact{ID: id})
		}
		for _, id := range graphs {
			td.Graphs = append(td.Graphs, ArtifactGraph{ArtifactID: id})
		}
		return td
	}
	for _, c := range []struct {
		name string
		td   TrainingDataset
		ok   bool
	}{
		{"whole", sub([]int64{1, 2, 3}, []int64{1, 2}, map[int64]string{1: "a", 2: "b"}), true},
		{"subset", sub([]int64{2}, []int64{2}, map[int64]string{2: "b"}), true},
		{"empty", sub(nil, nil, nil), true},
		{"artifact not in the dataset", sub([]int64{4}, nil, nil), false},
		{"artifact twice", sub([]int64{1, 1}, nil, nil), false},
		{"graph not in the dataset", sub([]int64{3}, []int64{3}, map[int64]string{3: "a"}), false},
		{"graph twice", sub([]int64{1}, []int64{1, 1}, map[int64]string{1: "a"}), false},
		{"graph without its artifact", sub([]int64{2}, []int64{1}, map[int64]string{1: "a"}), false},
		{"graph without a class", sub([]int64{1}, []int64{1}, nil), false},
		{"blank class", sub([]int64{1}, []int64{1}, map[int64]string{1: " "}), false},
		{"class without a graph", sub([]int64{1, 2}, []int64{1}, map[int64]string{1: "a", 2: "b"}), false},
	} {
		err := ValidateTrainingDataset(in, c.td)
		if c.ok && err != nil || !c.ok && !errors.Is(err, ErrInvalidTrainingDataset) {
			t.Errorf("%s: err = %v, want ok = %v", c.name, err, c.ok)
		}
	}
}

func TestCallClassifyingTrainingHook(t *testing.T) {
	in := KnowledgeDataset{Artifacts: []graph.Artifact{{ID: 1}}}
	ctx := context.Background()
	if _, err := CallClassifyingTrainingHook(ctx, func(context.Context, KnowledgeDataset) (TrainingDataset, error) {
		panic("bug")
	}, in); err == nil {
		t.Error("a panic is not an error")
	}
	causeErr := errors.New("down")
	if _, err := CallClassifyingTrainingHook(ctx, func(context.Context, KnowledgeDataset) (TrainingDataset, error) {
		return TrainingDataset{}, causeErr
	}, in); !errors.Is(err, causeErr) {
		t.Errorf("err = %v, want it to wrap %v", err, causeErr)
	}
	td, err := CallClassifyingTrainingHook(ctx, func(_ context.Context, ds KnowledgeDataset) (TrainingDataset, error) {
		return TrainingDataset{KnowledgeDataset: ds}, nil
	}, in)
	if err != nil || len(td.Artifacts) != 1 {
		t.Errorf("td = %+v, err = %v", td, err)
	}
}
