// Package db opens the database selected in the config and routes every database operation to the backend
// subpackage in use (sqlite or postgres). All SQL lives in the backends (internal/db/sqlite, internal/db/postgres);
// other packages only call the functions here and never write SQL. Each backend owns its schema, its SQL dialect
// and its timestamp storage format.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"

	"github.com/ivan/gligabao/internal/config"
	"github.com/ivan/gligabao/internal/db/model"
	"github.com/ivan/gligabao/internal/db/postgres"
	"github.com/ivan/gligabao/internal/db/sqlite"
)

// Types shared with the backends (see package model).
type (
	Dataset        = model.Dataset
	Entity         = model.Entity
	Edge           = model.Edge
	EvidenceEntity = model.EvidenceEntity
	EvidenceEdge   = model.EvidenceEdge
	NewEdge        = model.NewEdge
	Tx             = model.Tx
)

// Errors returned by the functions below.
var (
	ErrNotFound = model.ErrNotFound
	ErrExists   = model.ErrExists
)

//---------------------------------------------------------------------------------------------

// active is the backend selected by the last successful Open.
var active atomic.Value // string

func backend() string {
	if b, _ := active.Load().(string); b != "" {
		return b
	}
	return config.DBBackendSQLite
}

func unsupported() error { return fmt.Errorf("unknown db backend %q", backend()) }

// Open opens the database of the given backend (config.DBBackendSQLite or config.DBBackendPostgres; empty means
// sqlite), applies its schema, and makes it the backend all other functions of this package route to.
// For sqlite, dsn is the database file path; for postgres it is a connection URL.
func Open(backendName, dsn string) (*sql.DB, error) {
	if backendName == "" {
		backendName = config.DBBackendSQLite
	}
	var (
		d   *sql.DB
		err error
	)
	switch backendName {
	case config.DBBackendSQLite:
		d, err = sqlite.Open(dsn)
	case config.DBBackendPostgres:
		d, err = postgres.Open(dsn)
	default:
		return nil, fmt.Errorf("unknown db backend %q", backendName)
	}
	if err != nil {
		return nil, err
	}
	active.Store(backendName)
	return d, nil
}

//---------------------------------------------------------------------------------------------
// Datasets

// CreateDataset creates a dataset owned by userID (inputs already validated). It returns ErrExists if the user
// already has a live dataset with that name.
func CreateDataset(ctx context.Context, d *sql.DB, userID, name, description string, tags []string) (*Dataset, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.CreateDataset(ctx, d, userID, name, description, tags)
	case config.DBBackendPostgres:
		return postgres.CreateDataset(ctx, d, userID, name, description, tags)
	}
	return nil, unsupported()
}

// ListDatasets returns userID's datasets oldest first; deleted ones only if includeDeleted.
func ListDatasets(ctx context.Context, d *sql.DB, userID string, includeDeleted bool) ([]Dataset, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListDatasets(ctx, d, userID, includeDeleted)
	case config.DBBackendPostgres:
		return postgres.ListDatasets(ctx, d, userID, includeDeleted)
	}
	return nil, unsupported()
}

// DeleteDataset soft-deletes userID's live dataset called name. It returns ErrNotFound if there is none.
func DeleteDataset(ctx context.Context, d *sql.DB, userID, name string) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.DeleteDataset(ctx, d, userID, name)
	case config.DBBackendPostgres:
		return postgres.DeleteDataset(ctx, d, userID, name)
	}
	return unsupported()
}

// DatasetID returns the ID of userID's live dataset called name, or ErrNotFound.
func DatasetID(ctx context.Context, d *sql.DB, userID, name string) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.DatasetID(ctx, d, userID, name)
	case config.DBBackendPostgres:
		return postgres.DatasetID(ctx, d, userID, name)
	}
	return 0, unsupported()
}

//---------------------------------------------------------------------------------------------
// Writing a memory

// Begin starts a transaction on the backend in use. The returned Tx is that backend's implementation.
func Begin(ctx context.Context, d *sql.DB) (Tx, error) {
	switch backend() {
	case config.DBBackendSQLite:
		tx, err := sqlite.Begin(ctx, d)
		if err != nil {
			return nil, err
		}
		return tx, nil
	case config.DBBackendPostgres:
		tx, err := postgres.Begin(ctx, d)
		if err != nil {
			return nil, err
		}
		return tx, nil
	}
	return nil, unsupported()
}

// RecordLLMCall stores one LLM/embedding API call.
func RecordLLMCall(ctx context.Context, d *sql.DB, kind, provider, modelName string, inputTokens, outputTokens int, costUSD float64) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.RecordLLMCall(ctx, d, kind, provider, modelName, inputTokens, outputTokens, costUSD)
	case config.DBBackendPostgres:
		return postgres.RecordLLMCall(ctx, d, kind, provider, modelName, inputTokens, outputTokens, costUSD)
	}
	return unsupported()
}

//---------------------------------------------------------------------------------------------
// Reading edges and the graph

// LoadCandidateEdges returns live (not expired) edges of the dataset touching at least one entity whose normalized
// name is in nameKeys, newest first, at most limit.
func LoadCandidateEdges(ctx context.Context, d *sql.DB, dsID int64, nameKeys []string, limit int) ([]Edge, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.LoadCandidateEdges(ctx, d, dsID, nameKeys, limit)
	case config.DBBackendPostgres:
		return postgres.LoadCandidateEdges(ctx, d, dsID, nameKeys, limit)
	}
	return nil, unsupported()
}

// ListEdges returns every edge (expired ones included) of userID's live dataset called name, by ID.
func ListEdges(ctx context.Context, d *sql.DB, userID, name string) ([]Edge, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListEdges(ctx, d, userID, name)
	case config.DBBackendPostgres:
		return postgres.ListEdges(ctx, d, userID, name)
	}
	return nil, unsupported()
}

// FallbackSeedEntities returns the IDs of the most recently updated entities of the dataset.
func FallbackSeedEntities(ctx context.Context, d *sql.DB, dsID int64, limit int) ([]int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FallbackSeedEntities(ctx, d, dsID, limit)
	case config.DBBackendPostgres:
		return postgres.FallbackSeedEntities(ctx, d, dsID, limit)
	}
	return nil, unsupported()
}

// MatchEntities returns the IDs of the best entities whose name or description mention the query terms, best
// first. queryKey is the normalized query; tokens are its distinct terms.
func MatchEntities(ctx context.Context, d *sql.DB, dsID int64, queryKey string, tokens []string, limit int) ([]int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.MatchEntities(ctx, d, dsID, queryKey, tokens, limit)
	case config.DBBackendPostgres:
		return postgres.MatchEntities(ctx, d, dsID, queryKey, tokens, limit)
	}
	return nil, unsupported()
}

// EdgesAround returns the edges of the dataset that start or end at one of the entity IDs, newest first.
func EdgesAround(ctx context.Context, d *sql.DB, dsID int64, entityIDs []int64, limit int) ([]EvidenceEdge, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.EdgesAround(ctx, d, dsID, entityIDs, limit)
	case config.DBBackendPostgres:
		return postgres.EdgesAround(ctx, d, dsID, entityIDs, limit)
	}
	return nil, unsupported()
}

// EntitiesByID returns the entities with the given IDs.
func EntitiesByID(ctx context.Context, d *sql.DB, ids []int64) ([]EvidenceEntity, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.EntitiesByID(ctx, d, ids)
	case config.DBBackendPostgres:
		return postgres.EntitiesByID(ctx, d, ids)
	}
	return nil, unsupported()
}

//---------------------------------------------------------------------------------------------
// Inspection (read-only summaries of what is stored; used to verify behaviour, e.g. in tests)

type (
	Memory      = model.Memory
	Counts      = model.DatasetCounts
	LLMCallStat = model.LLMCallStat
)

// DatasetCounts returns the number of memories, entities and edges in the dataset.
func DatasetCounts(ctx context.Context, d *sql.DB, dsID int64) (Counts, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.DatasetCounts(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.DatasetCounts(ctx, d, dsID)
	}
	return Counts{}, unsupported()
}

// ListMemories returns the episodes of the dataset by ID.
func ListMemories(ctx context.Context, d *sql.DB, dsID int64) ([]Memory, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListMemories(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.ListMemories(ctx, d, dsID)
	}
	return nil, unsupported()
}

// ListEntities returns the entities of the dataset by ID.
func ListEntities(ctx context.Context, d *sql.DB, dsID int64) ([]EvidenceEntity, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListEntities(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.ListEntities(ctx, d, dsID)
	}
	return nil, unsupported()
}

// ListGraphEdges returns every edge of the dataset (expired ones included) with its endpoint IDs and names, by ID.
func ListGraphEdges(ctx context.Context, d *sql.DB, dsID int64) ([]EvidenceEdge, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListGraphEdges(ctx, d, dsID)
	case config.DBBackendPostgres:
		return postgres.ListGraphEdges(ctx, d, dsID)
	}
	return nil, unsupported()
}

// CountCrossDatasetEdges returns the number of edges whose dataset differs from that of one of their endpoints
// (always 0 unless datasets leak into each other).
func CountCrossDatasetEdges(ctx context.Context, d *sql.DB) (int, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.CountCrossDatasetEdges(ctx, d)
	case config.DBBackendPostgres:
		return postgres.CountCrossDatasetEdges(ctx, d)
	}
	return 0, unsupported()
}

// LLMCallStats sums the logged LLM calls by type.
func LLMCallStats(ctx context.Context, d *sql.DB) (map[string]LLMCallStat, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.LLMCallStats(ctx, d)
	case config.DBBackendPostgres:
		return postgres.LLMCallStats(ctx, d)
	}
	return nil, unsupported()
}
