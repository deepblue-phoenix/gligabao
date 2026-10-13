package memory

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

// GraphExport is a whole dataset graph: its entities (nodes) and every edge (facts, expired ones included).
type GraphExport struct {
	Dataset  string                 `json:"dataset"`
	Entities []graph.EvidenceEntity `json:"entities"`
	Edges    []graph.EvidenceEdge   `json:"edges"`
	// Models are the models (gb_models) that inferred the entities and edges, which refer to them by model_id.
	Models []relational.Model `json:"models"`
}

// ExportGraph returns the full graph of userID's live dataset called name (empty means the default dataset).
// It returns ErrDatasetNotFound if there is no such dataset.
func ExportGraph(ctx context.Context, d *sql.DB, userID, name string) (*GraphExport, error) {
	userID, err := ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	name, err = ResolveDatasetName(name)
	if err != nil {
		return nil, err
	}
	dsID, err := datasetID(ctx, d, userID, name)
	if err != nil {
		return nil, err
	}
	return exportGraph(ctx, d, dsID, name)
}

// ExportDatasetGraph returns the full graph of the dataset with the given ID and name, which may be a deleted one
// (its rows are kept). It is meant for admin tooling that follows a stored dataset ID, such as an eval run's.
func ExportDatasetGraph(ctx context.Context, d *sql.DB, ds *relational.Dataset) (*GraphExport, error) {
	return exportGraph(ctx, d, ds.ID, ds.Name)
}

func exportGraph(ctx context.Context, d *sql.DB, dsID int64, name string) (*GraphExport, error) {
	entities, err := graph.ListEntities(ctx, d, dsID)
	if err != nil {
		return nil, err
	}
	edges, err := graph.ListGraphEdges(ctx, d, dsID)
	if err != nil {
		return nil, err
	}
	if entities == nil {
		entities = []graph.EvidenceEntity{}
	}
	if edges == nil {
		edges = []graph.EvidenceEdge{}
	}
	models, err := referencedModels(ctx, d, entities, edges)
	if err != nil {
		return nil, err
	}
	return &GraphExport{Dataset: name, Entities: entities, Edges: edges, Models: models}, nil
}

// referencedModels returns the stored models that the entities and edges refer to, by name.
func referencedModels(ctx context.Context, d *sql.DB, entities []graph.EvidenceEntity, edges []graph.EvidenceEdge) ([]relational.Model, error) {
	used := map[string]bool{}
	for _, e := range entities {
		used[e.ModelID] = true
	}
	for _, e := range edges {
		used[e.ModelID] = true
	}
	all, err := relational.ListModels(ctx, d)
	if err != nil {
		return nil, err
	}
	out := []relational.Model{}
	for _, m := range all {
		if used[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}
