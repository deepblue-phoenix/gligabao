package relational

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

//---------------------------------------------------------------------------------------------
// Remember runs (the gb_remember_runs table, see sqlite/db_schema.go; package memory_remember keeps it up to date)

// CreateRememberRun stores a remember run (r.ID is ignored; see model.NewRememberRun) and returns its ID. Call it outside
// any open transaction.
func CreateRememberRun(ctx context.Context, d *sql.DB, r RememberRun) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertRememberRun(ctx, d, r)
	case config.DBBackendPostgres:
		return postgres.InsertRememberRun(ctx, d, r)
	}
	return 0, unsupported()
}

// SaveRememberRun overwrites the stored remember run r.ID with all of r (the run and every stage), or returns ErrNotFound.
// Call it outside any open transaction.
func SaveRememberRun(ctx context.Context, d *sql.DB, r RememberRun) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.SaveRememberRun(ctx, d, r)
	case config.DBBackendPostgres:
		return postgres.SaveRememberRun(ctx, d, r)
	}
	return unsupported()
}

// GetRememberRun returns the remember run with that ID, or ErrNotFound.
func GetRememberRun(ctx context.Context, d *sql.DB, id int64) (*RememberRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetRememberRun(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetRememberRun(ctx, d, id)
	}
	return nil, unsupported()
}

// ListRememberRuns returns the remember runs that match f, newest first: at most limit of them, after skipping offset.
// total is how many match f, whatever limit and offset.
func ListRememberRuns(ctx context.Context, d *sql.DB, f RememberRunFilter, limit, offset int) (runs []RememberRun, total int, err error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListRememberRuns(ctx, d, f, limit, offset)
	case config.DBBackendPostgres:
		return postgres.ListRememberRuns(ctx, d, f, limit, offset)
	}
	return nil, 0, unsupported()
}
