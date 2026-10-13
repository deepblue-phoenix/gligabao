package relational

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------
// Models (the gb_models table, see sqlite/db_schema.go)

// ModelID returns the ID of the model called name (see model.ModelID): derived from the name alone, so it is the same
// in every database; "" for an empty name.
func ModelID(name string) string { return model.ModelID(name) }

// SyncModels makes models the supported models of the database: each is inserted (or updated) as supported and the
// supported flag of every other stored model is cleared (those rows stay, entities and edges may reference them).
// It is called on every startup. The ID and Supported fields of models are ignored.
func SyncModels(ctx context.Context, d *sql.DB, models []Model) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.SyncModels(ctx, d, models)
	case config.DBBackendPostgres:
		return postgres.SyncModels(ctx, d, models)
	}
	return unsupported()
}

// EnsureModel stores the model (as not supported) unless it is already stored, and returns its ID ("" for an empty
// name). Call it before storing anything that references the model, outside any open transaction.
func EnsureModel(ctx context.Context, d *sql.DB, m Model) (string, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.EnsureModel(ctx, d, m)
	case config.DBBackendPostgres:
		return postgres.EnsureModel(ctx, d, m)
	}
	return "", unsupported()
}

// ListModels returns every stored model by name.
func ListModels(ctx context.Context, d *sql.DB) ([]Model, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListModels(ctx, d)
	case config.DBBackendPostgres:
		return postgres.ListModels(ctx, d)
	}
	return nil, unsupported()
}
