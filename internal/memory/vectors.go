package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// WithVectors makes Remember embed the entities and edges it stores and write the embeddings to store, into the
// dataset's vector datasets (one per index, see VectorDataset). Without it (or if either argument is nil)
// Remember does not touch vectors.
func WithVectors(store vector.Store, emb memory_core.Embedder) memory_core.Option {
	return func(o *memory_core.CallOptions) { o.Vectors, o.Embedder = store, emb }
}

// Values of the metadata key "kind" of the vectors written here (one per index).
const (
	VectorKindEntity = "entity"
	VectorKindEdge   = "edge"
)

// EntityVectorID is the vector ID of an entity in the entity index of its dataset.
func EntityVectorID(entityID int64) string {
	return VectorKindEntity + "-" + strconv.FormatInt(entityID, 10)
}

// EdgeVectorID is the vector ID of an edge in the edge index of its dataset.
func EdgeVectorID(edgeID int64) string { return VectorKindEdge + "-" + strconv.FormatInt(edgeID, 10) }

//---------------------------------------------------------------------------------------------

// IndexStats is the state of one vector index of a dataset.
type IndexStats struct {
	Index VectorIndex `json:"index"`
	// VectorDataset is the dataset's name in the vector store.
	VectorDataset string `json:"vector_dataset"`
	// Expected is the number of rows (entities, edges) that should have a vector in this index.
	Expected int `json:"expected"`
	vector.Stats
}

// DatasetVectorStats is the state of all the vector indexes of a dataset.
type DatasetVectorStats struct {
	// Backend is "" if there is no vector store, or it cannot report statistics.
	Backend string       `json:"backend"`
	Indexes []IndexStats `json:"indexes"`
}

// VectorStats reports, for every vector index of userID's live dataset name, the vectors the store holds next to the
// number of rows that should have one. vectors may be nil (no indexes are listed then).
// It returns ErrDatasetNotFound if there is no such dataset.
func VectorStats(ctx context.Context, d *sql.DB, vectors vector.Store, userID, name string) (*DatasetVectorStats, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	name, err = memory_core.ResolveDatasetName(name)
	if err != nil {
		return nil, err
	}
	id, err := datasetID(ctx, d, userID, name)
	if err != nil {
		return nil, err
	}
	out := &DatasetVectorStats{Indexes: []IndexStats{}}
	statter, ok := vectors.(vector.Statter)
	if !ok {
		return out, nil
	}
	counts, err := graph.DatasetCounts(ctx, d, id)
	if err != nil {
		return nil, err
	}
	for _, idx := range VectorIndexes {
		ds := VectorDataset(id, idx)
		st, err := statter.Stats(ctx, ds)
		if err != nil {
			return nil, fmt.Errorf("memory: stats of vector dataset %s: %w", ds, err)
		}
		expected := counts.Entities
		if idx == IndexEdges {
			expected = counts.Edges
		}
		out.Backend = st.Backend
		out.Indexes = append(out.Indexes, IndexStats{Index: idx, VectorDataset: ds, Expected: expected, Stats: st})
	}
	return out, nil
}
