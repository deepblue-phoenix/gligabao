package evals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db/sqlite"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// StoreFor returns the functions of a database backend (config.DBBackendSQLite or config.DBBackendPostgres; empty is sqlite).
func StoreFor(backend string) (db.Store, error) {
	switch backend {
	case "", config.DBBackendSQLite:
		return sqlite.Store, nil
	case config.DBBackendPostgres:
		return postgres.Store, nil
	}
	return db.Store{}, fmt.Errorf("unknown evals db backend %q", backend)
}

// postgresSchemaLock is the advisory lock key that serializes the starters that apply the schema (CREATE ... IF NOT EXISTS can
// still race in PostgreSQL).
const postgresSchemaLock = 0x67620008

// oldTables lists every gb_* table of the evals schema, children first (the order to drop them in).
var oldTables = []string{
	"gb_eval_bookmarks",
	"gb_case_run_commentaries",
	"gb_case_runs_meta_knowledge_trajectories",
	"gb_case_runs_meta_training",
	"gb_case_runs_meta_knowledge",
	"gb_case_runs",
	"gb_eval_runs_meta_training",
	"gb_eval_runs_meta_knowledge",
	"gb_eval_runs",
	"gb_experiment_runs",
	"gb_experiment_def_evals",
	"gb_case_def_meta_training",
	"gb_case_def_meta_knowledge",
	"gb_case_defs",
	"gb_experiment_defs",
	"gb_eval_defs",
}

// Open applies the schema of the evals tables (gb_*) to d, the relational database opened for the backend
// (config.DBBackendSQLite or config.DBBackendPostgres; empty is sqlite). The statements are idempotent. The tables reference
// nothing outside the evals schema, so it can be applied at any point of the opening of the database.
//
// The tables of a database from before the evals had an owner (gb_eval_defs without user_id) have the old shape, which CREATE TABLE
// IF NOT EXISTS would keep: if they are all empty they are dropped and created anew, else Open fails and the operator drops them
// by hand (see DropOldTables).
func Open(backend string,
	d *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch backend {
	case "", config.DBBackendSQLite:
		if err := DropOldTables(ctx,
			d,
			true); err != nil {
			return err
		}
		if _, err := d.ExecContext(ctx, sqlite.Schema); err != nil {
			return fmt.Errorf("evals: init schema: %w", err)
		}
		return nil
	case config.DBBackendPostgres:
		conn, err := d.Conn(ctx)
		if err != nil {
			return fmt.Errorf("evals: init schema: %w", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx,
			`SELECT pg_advisory_lock($1)`,
			int64(postgresSchemaLock)); err != nil {
			return fmt.Errorf("evals: init schema: lock: %w", err)
		}
		defer conn.ExecContext(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`,
			int64(postgresSchemaLock))
		if err := DropOldTables(ctx,
			conn,
			false); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, postgres.Schema); err != nil {
			return fmt.Errorf("evals: init schema: %w", err)
		}
		return nil
	}
	return fmt.Errorf("unknown evals db backend %q", backend)
}

// DropOldTables is the guard of Open: if gb_eval_defs exists without a user_id column (the shape from before the evals had an
// owner), it drops every evals table when all of them are empty, and returns an error that tells the operator to drop them by hand
// when one has rows. It does nothing when the tables do not exist or are of the new shape. sqlite says which backend q is.
func DropOldTables(ctx context.Context,
	q db.Execer,
	sqlite bool) error {
	hasTable, hasOwner, err := ownerColumn(ctx,
		q,
		sqlite)
	if err != nil {
		return err
	}
	if !hasTable || hasOwner {
		return nil
	}
	for _, t := range oldTables {
		var exists int
		err := q.QueryRowContext(ctx, "SELECT 1 FROM "+t+" LIMIT 1").Scan(&exists)
		switch {
		case err == nil:
			return fmt.Errorf("evals: the table %s has rows and the evals tables have the old shape (no owner): drop the gb_* evals tables by hand (%s) and restart",
				t,
				strings.Join(oldTables, ", "))
		case errors.Is(err, sql.ErrNoRows):
		case isMissingTable(err):
		default:
			return fmt.Errorf("evals: check the old table %s: %w",
				t,
				err)
		}
	}
	suffix := " CASCADE"
	if sqlite {
		suffix = ""
	}
	for _, t := range oldTables {
		if _, err := q.ExecContext(ctx, "DROP TABLE IF EXISTS "+t+suffix); err != nil {
			return fmt.Errorf("evals: drop the old table %s: %w",
				t,
				err)
		}
	}
	return nil
}

// isMissingTable reports whether err says that a table does not exist (the old schema may lack some of the tables).
func isMissingTable(err error) bool {
	m := err.Error()
	return strings.Contains(m, "no such table") || strings.Contains(m, "does not exist")
}

// ownerColumn reports whether gb_eval_defs exists and whether it has the user_id column.
func ownerColumn(ctx context.Context,
	q db.Execer,
	sqlite bool) (hasTable, hasOwner bool, err error) {
	query := `SELECT COUNT(*), COALESCE(SUM(CASE WHEN column_name = 'user_id' THEN 1 ELSE 0 END), 0)
		FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'gb_eval_defs'`
	if sqlite {
		query = `SELECT COUNT(*), COALESCE(SUM(CASE WHEN name = 'user_id' THEN 1 ELSE 0 END), 0) FROM pragma_table_info('gb_eval_defs')`
	}
	var cols, owner int
	if err := q.QueryRowContext(ctx, query).Scan(&cols, &owner); err != nil {
		return false, false, fmt.Errorf("evals: check the owner column: %w", err)
	}
	return cols > 0, owner > 0, nil
}
