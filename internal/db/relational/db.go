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

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

// Types shared with the backends (see package model).
type (
	Dataset     = model.Dataset
	LLMCallStat = model.LLMCallStat
	Ontology    = model.Ontology
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

// ListAllDatasets returns the datasets of all users, oldest first; deleted ones only if includeDeleted.
// It is meant for admin tooling; the user-facing API only ever lists a single user's datasets.
func ListAllDatasets(ctx context.Context, d *sql.DB, includeDeleted bool) ([]Dataset, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListAllDatasets(ctx, d, includeDeleted)
	case config.DBBackendPostgres:
		return postgres.ListAllDatasets(ctx, d, includeDeleted)
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
// Ontologies (metadata only; the classes and relationships are stored by package graph)

// CreateOntology stores the metadata of an ontology (o.ID and o.CreatedAt are ignored and filled in the result).
// It returns ErrExists if the user already has an ontology with that name.
func CreateOntology(ctx context.Context, d *sql.DB, o Ontology) (*Ontology, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.CreateOntology(ctx, d, o)
	case config.DBBackendPostgres:
		return postgres.CreateOntology(ctx, d, o)
	}
	return nil, unsupported()
}

// GetOntology returns the metadata of userID's ontology called name, or ErrNotFound.
func GetOntology(ctx context.Context, d *sql.DB, userID, name string) (*Ontology, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetOntology(ctx, d, userID, name)
	case config.DBBackendPostgres:
		return postgres.GetOntology(ctx, d, userID, name)
	}
	return nil, unsupported()
}

// UpdateOntology overwrites the metadata of the ontology o.ID with the other fields of o (description, IRI, title,
// format, size, hash, counts and UpdatedAt); the owner, name and creation time never change. It returns the stored
// row, or ErrNotFound.
func UpdateOntology(ctx context.Context, d *sql.DB, o Ontology) (*Ontology, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.UpdateOntology(ctx, d, o)
	case config.DBBackendPostgres:
		return postgres.UpdateOntology(ctx, d, o)
	}
	return nil, unsupported()
}

// ListOntologies returns userID's ontologies, oldest first.
func ListOntologies(ctx context.Context, d *sql.DB, userID string) ([]Ontology, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListOntologies(ctx, d, userID)
	case config.DBBackendPostgres:
		return postgres.ListOntologies(ctx, d, userID)
	}
	return nil, unsupported()
}

// DeleteOntology removes the metadata row of an ontology (and, through the foreign keys, whatever graph rows
// still reference it). It is used to undo a failed load.
func DeleteOntology(ctx context.Context, d *sql.DB, id int64) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.DeleteOntology(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.DeleteOntology(ctx, d, id)
	}
	return unsupported()
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
