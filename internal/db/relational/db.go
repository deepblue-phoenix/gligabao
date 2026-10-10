// Package relational opens the database selected in the config and routes every database operation to the backend
// subpackage in use (sqlite or postgres). It covers datasets and the LLM call log; graph operations are in internal/db/graph. All SQL lives in the backends (internal/db/relational/sqlite, internal/db/relational/postgres);
// other packages only call the functions here and never write SQL. Each backend owns its schema, its SQL dialect
// and its timestamp storage format.
package relational

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"

	"github.com/ivan/gligabao/internal/config"
	"github.com/ivan/gligabao/internal/db/relational/model"
	"github.com/ivan/gligabao/internal/db/relational/postgres"
	"github.com/ivan/gligabao/internal/db/relational/sqlite"
)

// Types shared with the backends (see package model).
type (
	Dataset     = model.Dataset
	LLMCallStat = model.LLMCallStat
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

// EnsureDataset returns the ID of userID's live dataset called name, creating it first if needed.
func EnsureDataset(ctx context.Context, d *sql.DB, userID, name string) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.EnsureDataset(ctx, d, userID, name)
	case config.DBBackendPostgres:
		return postgres.EnsureDataset(ctx, d, userID, name)
	}
	return 0, unsupported()
}

//---------------------------------------------------------------------------------------------
// LLM call log

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
