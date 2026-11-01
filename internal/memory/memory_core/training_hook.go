package memory_core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

// The classifying training hook: a callback of the program that embeds gligabao (gligabao.ServeOptions.ClassifyingTrainingHook, see
// docs/hooks.md) that makes the training dataset of a classifier of graphs out of a knowledge dataset. gligabao hands it the whole
// dataset (every artifact and the graph of each), and it answers with a TrainingDataset: the knowledge dataset it wants to train on,
// which can be the whole of it or a filtered subset, plus the class of each of the graphs of it.
//
// The hook is the only classifying training there is for now; other kinds of training will get hooks of their own.

// ArtifactGraph is the graph that comes with one artifact of a dataset: the entities that mention it (the ones it created and the ones it
// found already stored) and the facts it stated (the ones it created and those whose duplicate it was merged into), in their stored state
// (the same links as docs/artifacts.md#provenance-links). An artifact that yielded nothing has no ArtifactGraph.
type ArtifactGraph struct {
	// ArtifactID is the artifact the graph is of; it is also what identifies the graph (a dataset has at most one per artifact).
	ArtifactID int64
	Entities   []graph.EvidenceEntity
	Edges      []graph.EvidenceEdge
	// Classes are the classes an LLM gave to the graph when it was remembered (the classify_graph stage, see graph_classes.go): the records
	// of gb_artifact_classes of the artifact, in the order they were made (by ID). Each says which model run gave it (InferenceID). Empty
	// if the graph was not classified. They are what a hook is expected to train on: it picks the class of each graph from them.
	Classes []ArtifactClass
}

// ArtifactClass is one class an LLM gave to the graph of an artifact (a record of gb_artifact_classes), with the run of the model call.
type ArtifactClass = graph.ArtifactClass

// KnowledgeDataset is what a dataset has stored: all its artifacts (of every kind: texts, chunks, web searches, ...) and the graphs that
// come with them.
type KnowledgeDataset struct {
	// UserID and Dataset (already resolved) say which dataset it is; DatasetID is its ID.
	UserID, Dataset string
	DatasetID       int64
	// Artifacts are all the artifacts of the dataset, in the order of their IDs.
	Artifacts []graph.Artifact
	// Graphs are the graphs of the artifacts that have one, in the order of the artifact IDs.
	Graphs []ArtifactGraph
}

// TrainingDataset is the superset of a KnowledgeDataset that a training works from: the knowledge dataset (all of it, or a filtered
// subset) and, for each of the graphs in it, the class it belongs to.
type TrainingDataset struct {
	KnowledgeDataset
	// Classes has the class of each graph of the dataset, by the ID of the artifact the graph is of (ArtifactGraph.ArtifactID): every
	// graph has one, and a class is a non-empty name.
	Classes map[int64]string
}

// ErrInvalidTrainingDataset means the training dataset a training hook returned is not a valid one (see ClassifyingTrainingHook).
var ErrInvalidTrainingDataset = errors.New("invalid training dataset")

// ClassifyingTrainingHook is the classifying training hook: it gets the whole knowledge dataset and returns the training dataset to train a
// classifier of graphs on. It does not have to return all of it: the artifacts and graphs it leaves out are not trained on (a filter, a
// sample, a split). It can only choose from what it got: it must not add an artifact or a graph, or change them (gligabao checks the IDs,
// see ValidateTrainingDataset). It must honour ctx, may be called concurrently, and must not modify what it is given.
type ClassifyingTrainingHook func(ctx context.Context, ds KnowledgeDataset) (TrainingDataset, error)

// ValidateTrainingDataset checks that td, which a hook made from in, is a valid training dataset. It returns an error that wraps
// ErrInvalidTrainingDataset if:
//   - td has an artifact or a graph that is not in in (by artifact ID), or the same one twice;
//   - td has a graph of an artifact that td does not have (a graph needs its artifact);
//   - a graph of td has no class, or the class is empty (blank), or td has a class for something that is not one of its graphs.
func ValidateTrainingDataset(in KnowledgeDataset, td TrainingDataset) error {
	artifacts := make(map[int64]bool, len(in.Artifacts))
	for _, a := range in.Artifacts {
		artifacts[a.ID] = true
	}
	graphs := make(map[int64]bool, len(in.Graphs))
	for _, g := range in.Graphs {
		graphs[g.ArtifactID] = true
	}
	kept := make(map[int64]bool, len(td.Artifacts))
	for _, a := range td.Artifacts {
		if !artifacts[a.ID] {
			return fmt.Errorf("%w: artifact %d is not in the dataset", ErrInvalidTrainingDataset, a.ID)
		}
		if kept[a.ID] {
			return fmt.Errorf("%w: artifact %d is twice in the dataset", ErrInvalidTrainingDataset, a.ID)
		}
		kept[a.ID] = true
	}
	keptGraphs := make(map[int64]bool, len(td.Graphs))
	for _, g := range td.Graphs {
		switch {
		case !graphs[g.ArtifactID]:
			return fmt.Errorf("%w: the graph of artifact %d is not in the dataset", ErrInvalidTrainingDataset, g.ArtifactID)
		case keptGraphs[g.ArtifactID]:
			return fmt.Errorf("%w: the graph of artifact %d is twice in the dataset", ErrInvalidTrainingDataset, g.ArtifactID)
		case !kept[g.ArtifactID]:
			return fmt.Errorf("%w: the graph of artifact %d is kept but the artifact is not", ErrInvalidTrainingDataset, g.ArtifactID)
		}
		keptGraphs[g.ArtifactID] = true
		if strings.TrimSpace(td.Classes[g.ArtifactID]) == "" {
			return fmt.Errorf("%w: the graph of artifact %d has no class", ErrInvalidTrainingDataset, g.ArtifactID)
		}
	}
	for id := range td.Classes {
		if !keptGraphs[id] {
			return fmt.Errorf("%w: there is a class for artifact %d, which has no graph in the dataset", ErrInvalidTrainingDataset, id)
		}
	}
	return nil
}

// CallClassifyingTrainingHook runs hook on ds, turning a panic of it into an error, and checks what it returned
// (ValidateTrainingDataset). Unlike the graph embedder, the hook is not advisory: its error (or panic) is returned, as is the
// ErrInvalidTrainingDataset of a bad result.
func CallClassifyingTrainingHook(ctx context.Context, hook ClassifyingTrainingHook, ds KnowledgeDataset) (td TrainingDataset, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			td, err = TrainingDataset{}, fmt.Errorf("training hook panic: %v", rec)
		}
	}()
	td, err = hook(ctx, ds)
	if err != nil {
		return TrainingDataset{}, fmt.Errorf("training hook: %w", err)
	}
	if err := ValidateTrainingDataset(ds, td); err != nil {
		return TrainingDataset{}, err
	}
	return td, nil
}
