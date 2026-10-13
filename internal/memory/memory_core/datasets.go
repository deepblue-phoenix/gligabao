package memory_core

// Datasets are hard logical containers: every memory, entity and edge belongs to exactly
// one dataset, and the same entity in two datasets is two separate rows. Nothing is ever linked or queried
// across datasets. Deleting a dataset is soft (gb_datasets.deleted = 1): its rows stay, but it can no longer be
// read or written, and its name can be reused by a new, empty dataset.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"

	log "github.com/sirupsen/logrus"
)

var ErrDatasetExists = errors.New("dataset already exists")

// Dataset describes a dataset.
type Dataset = relational.Dataset

func normTags(tags []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

//---------------------------------------------------------------------------------------------

// CreateDataset creates a dataset owned by userID. The name is required (no default is applied). It returns
// ErrDatasetExists if a live dataset with that name exists for the user.
func CreateDataset(ctx context.Context, d *sql.DB, userID, name, description string, tags []string) (*Dataset, error) {
	userID, err := ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidDataset)
	}
	name, err = ResolveDatasetName(name)
	if err != nil {
		return nil, err
	}
	ds, err := relational.CreateDataset(ctx, d, userID, name, strings.TrimSpace(description), normTags(tags))
	if errors.Is(err, relational.ErrExists) {
		return nil, ErrDatasetExists
	}
	return ds, err
}

// ListDatasets returns userID's datasets oldest first; deleted ones only if includeDeleted.
func ListDatasets(ctx context.Context, d *sql.DB, userID string, includeDeleted bool) ([]Dataset, error) {
	userID, err := ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	return relational.ListDatasets(ctx, d, userID, includeDeleted)
}

// VectorIndex is the purpose of a set of vectors of a dataset: what they are embeddings of. Each index of a
// dataset is a dataset of its own in the vector store (see VectorDataset).
type VectorIndex string

// The vector indexes of a gligabao dataset.
const (
	// IndexEntities holds one vector per entity (vector ID EntityVectorID).
	IndexEntities VectorIndex = "entities"
	// IndexEdges holds one vector per edge, embedding the fact's description (vector ID EdgeVectorID).
	IndexEdges VectorIndex = "edges"
	// IndexChunks holds one vector per chunk of a long text, embedding the chunk's text (vector ID ChunkVectorID).
	IndexChunks VectorIndex = "chunks"
)

// VectorIndexes lists every vector index a dataset can have.
var VectorIndexes = []VectorIndex{IndexEntities, IndexEdges, IndexChunks}

// VectorDataset is the name, in the vector store (package vector), of the dataset that holds one index of a
// gligabao dataset: "dataset-<id>-<index>", e.g. "dataset-7-entities". The vector store isolates vectors by
// dataset: a chromem collection, a Pinecone namespace. A gligabao dataset has one per purpose, and the name says
// which one it is. It is keyed by the gligabao dataset's ID (not its name), so datasets never share vectors, and a
// name reused after a soft delete starts with empty ones.
func VectorDataset(datasetID int64, index VectorIndex) string {
	return "dataset-" + strconv.FormatInt(datasetID, 10) + "-" + string(index)
}

// EntityDataset is the vector store dataset of the entity index of a dataset (VectorDataset with IndexEntities).
func EntityDataset(datasetID int64) string { return VectorDataset(datasetID, IndexEntities) }

// EdgeDataset is the vector store dataset of the edge index of a dataset (VectorDataset with IndexEdges).
func EdgeDataset(datasetID int64) string { return VectorDataset(datasetID, IndexEdges) }

// ChunkDataset is the vector store dataset of the chunk index of a dataset (VectorDataset with IndexChunks).
func ChunkDataset(datasetID int64) string { return VectorDataset(datasetID, IndexChunks) }

// DeleteDataset soft-deletes userID's live dataset called name (sets deleted = 1) and removes the dataset's vectors
// from vectors, which may be nil. The relational rows are kept; the vectors are not, because a deleted dataset can
// never be read again (its name gets a new dataset with a new ID) and they would only occupy memory/storage.
// It returns ErrDatasetNotFound if there is no such dataset.
//
// A failure to remove the vectors does not fail the call (the dataset is deleted either way and the vector datasets are
// unreachable); it is logged.
func DeleteDataset(ctx context.Context, d *sql.DB, vectors vector.Store, userID, name string) error {
	userID, err := ResolveUserID(userID)
	if err != nil {
		return err
	}
	name, err = ResolveDatasetName(name)
	if err != nil {
		return err
	}
	id, err := DatasetID(ctx, d, userID, name)
	if err != nil {
		return err
	}
	if err := relational.DeleteDataset(ctx, d, userID, name); errors.Is(err, relational.ErrNotFound) {
		return ErrDatasetNotFound
	} else if err != nil {
		return err
	}
	if vectors != nil {
		for _, idx := range VectorIndexes {
			if err := vectors.DeleteAll(ctx, VectorDataset(id, idx)); err != nil {
				log.WithError(err).WithFields(log.Fields{"dataset_id": id, "index": idx}).Error("delete dataset: removing vectors")
			}
		}
	}
	return nil
}
