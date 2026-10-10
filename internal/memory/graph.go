package memory

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

// GraphExport is a whole dataset graph: its entities (nodes) and every edge (facts, expired ones included).
type GraphExport struct {
	Dataset  string                 `json:"dataset"`
	Entities []graph.EvidenceEntity `json:"entities"`
	Edges    []graph.EvidenceEdge   `json:"edges"`
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
	return &GraphExport{Dataset: name, Entities: entities, Edges: edges}, nil
}
