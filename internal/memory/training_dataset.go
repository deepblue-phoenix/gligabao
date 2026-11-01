package memory

import (
	"context"
	"database/sql"
	"errors"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// ErrNoTrainingHook means a training dataset was asked for but there is no training hook to make it.
var ErrNoTrainingHook = errors.New("no training hook")

// LoadKnowledgeDataset reads the knowledge dataset of userID's live dataset called name (empty means the default dataset): all its
// artifacts and the graph of each artifact that has one. It returns ErrDatasetNotFound if there is no such dataset.
func LoadKnowledgeDataset(ctx context.Context, d *sql.DB, userID, name string) (memory_core.KnowledgeDataset, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return memory_core.KnowledgeDataset{}, err
	}
	name, err = memory_core.ResolveDatasetName(name)
	if err != nil {
		return memory_core.KnowledgeDataset{}, err
	}
	dsID, err := memory_core.DatasetID(ctx, d, userID, name)
	if err != nil {
		return memory_core.KnowledgeDataset{}, err
	}
	artifacts, err := graph.ListArtifacts(ctx, d, dsID)
	if err != nil {
		return memory_core.KnowledgeDataset{}, err
	}
	entities, err := graph.ListEntities(ctx, d, dsID)
	if err != nil {
		return memory_core.KnowledgeDataset{}, err
	}
	edges, err := graph.ListGraphEdges(ctx, d, dsID)
	if err != nil {
		return memory_core.KnowledgeDataset{}, err
	}
	entityLinks, err := graph.ListEntityArtifacts(ctx, d, dsID)
	if err != nil {
		return memory_core.KnowledgeDataset{}, err
	}
	edgeLinks, err := graph.ListEdgeArtifacts(ctx, d, dsID)
	if err != nil {
		return memory_core.KnowledgeDataset{}, err
	}
	if artifacts == nil {
		artifacts = []graph.Artifact{}
	}
	return memory_core.KnowledgeDataset{
		UserID: userID, Dataset: name, DatasetID: dsID,
		Artifacts: artifacts,
		Graphs:    artifactGraphs(artifacts, entities, edges, entityLinks, edgeLinks),
	}, nil
}

// artifactGraphs groups the entities and edges by the artifacts they are linked to. The result has one graph per artifact that has
// at least one entity or edge, in the order of artifacts; the entities and edges of a graph are in the order they were given.
func artifactGraphs(artifacts []graph.Artifact, entities []graph.EvidenceEntity, edges []graph.EvidenceEdge, entityLinks []graph.EntityArtifact, edgeLinks []graph.EdgeArtifact) []memory_core.ArtifactGraph {
	entityByID := make(map[int64]graph.EvidenceEntity, len(entities))
	for _, e := range entities {
		entityByID[e.ID] = e
	}
	edgeByID := make(map[int64]graph.EvidenceEdge, len(edges))
	for _, e := range edges {
		edgeByID[e.ID] = e
	}
	entityIDs := map[int64][]int64{} // artifact ID -> entity IDs
	for _, l := range entityLinks {
		entityIDs[l.ArtifactID] = append(entityIDs[l.ArtifactID], l.EntityID)
	}
	edgeIDs := map[int64][]int64{}
	for _, l := range edgeLinks {
		edgeIDs[l.ArtifactID] = append(edgeIDs[l.ArtifactID], l.EdgeID)
	}
	graphs := []memory_core.ArtifactGraph{}
	for _, a := range artifacts {
		g := memory_core.ArtifactGraph{ArtifactID: a.ID, Entities: []graph.EvidenceEntity{}, Edges: []graph.EvidenceEdge{}}
		for _, id := range entityIDs[a.ID] {
			if e, ok := entityByID[id]; ok {
				g.Entities = append(g.Entities, e)
			}
		}
		for _, id := range edgeIDs[a.ID] {
			if e, ok := edgeByID[id]; ok {
				g.Edges = append(g.Edges, e)
			}
		}
		if len(g.Entities)+len(g.Edges) > 0 {
			graphs = append(graphs, g)
		}
	}
	return graphs
}

// MakeTrainingDataset loads the knowledge dataset of userID's live dataset called name and asks hook for the training dataset of it (the
// whole dataset, or a filtered subset, with the class of each graph). The result is checked (memory_core.ValidateTrainingDataset): a bad
// one is an error that wraps memory_core.ErrInvalidTrainingDataset. An error of the hook is returned, as is a panic of it. Nothing is
// stored. It returns ErrNoTrainingHook for a nil hook and memory_core.ErrDatasetNotFound if there is no such dataset.
func MakeTrainingDataset(ctx context.Context, d *sql.DB, hook memory_core.ClassifyingTrainingHook, userID, name string) (memory_core.TrainingDataset, error) {
	if hook == nil {
		return memory_core.TrainingDataset{}, ErrNoTrainingHook
	}
	ds, err := LoadKnowledgeDataset(ctx, d, userID, name)
	if err != nil {
		return memory_core.TrainingDataset{}, err
	}
	return memory_core.CallClassifyingTrainingHook(ctx, hook, ds)
}
