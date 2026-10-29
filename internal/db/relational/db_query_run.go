package relational

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

//---------------------------------------------------------------------------------------------
// Query runs (the gb_query_runs table, see sqlite/db_schema.go; package memory_query keeps it up to date)

// CreateQueryRun stores a query run (r.ID is ignored; see model.NewQueryRun) and returns its ID. Call it outside any open
// transaction.
func CreateQueryRun(ctx context.Context, d *sql.DB, r QueryRun) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertQueryRun(ctx, d, r)
	case config.DBBackendPostgres:
		return postgres.InsertQueryRun(ctx, d, r)
	}
	return 0, unsupported()
}

// SaveQueryRun overwrites the stored query run r.ID with all of r (the run and every stage), or returns ErrNotFound. Call it
// outside any open transaction.
func SaveQueryRun(ctx context.Context, d *sql.DB, r QueryRun) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.SaveQueryRun(ctx, d, r)
	case config.DBBackendPostgres:
		return postgres.SaveQueryRun(ctx, d, r)
	}
	return unsupported()
}

// GetQueryRun returns the query run with that ID, or ErrNotFound.
func GetQueryRun(ctx context.Context, d *sql.DB, id int64) (*QueryRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetQueryRun(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetQueryRun(ctx, d, id)
	}
	return nil, unsupported()
}

// ListQueryRuns returns the query runs that match f, newest first: at most limit of them, after skipping offset. total is how
// many match f, whatever limit and offset.
func ListQueryRuns(ctx context.Context, d *sql.DB, f QueryRunFilter, limit, offset int) (runs []QueryRun, total int, err error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListQueryRuns(ctx, d, f, limit, offset)
	case config.DBBackendPostgres:
		return postgres.ListQueryRuns(ctx, d, f, limit, offset)
	}
	return nil, 0, unsupported()
}
