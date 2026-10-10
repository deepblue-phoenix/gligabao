package memory

import (
	"context"
	"database/sql"

	"github.com/ivan/gligabao/internal/db"
)

// GraphExport is a whole dataset graph: its entities (nodes) and every edge (facts, expired ones included).
type GraphExport struct {
	Dataset  string              `json:"dataset"`
	Entities []db.EvidenceEntity `json:"entities"`
	Edges    []db.EvidenceEdge   `json:"edges"`
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
	entities, err := db.ListEntities(ctx, d, dsID)
	if err != nil {
		return nil, err
	}
	edges, err := db.ListGraphEdges(ctx, d, dsID)
	if err != nil {
		return nil, err
	}
	if entities == nil {
		entities = []db.EvidenceEntity{}
	}
	if edges == nil {
		edges = []db.EvidenceEdge{}
	}
	return &GraphExport{Dataset: name, Entities: entities, Edges: edges}, nil
}
