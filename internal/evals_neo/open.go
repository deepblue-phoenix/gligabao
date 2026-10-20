package evals_neo

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/db/sqlite"
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

// Open applies the schema of the evals tables (gb_neo_*) to d, the relational database opened for the backend
// (config.DBBackendSQLite or config.DBBackendPostgres; empty is sqlite). The statements are idempotent. The tables reference
// nothing outside the evals schema, so it can be applied at any point of the opening of the database.
func Open(backend string, d *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch backend {
	case "", config.DBBackendSQLite:
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
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(postgresSchemaLock)); err != nil {
			return fmt.Errorf("evals: init schema: lock: %w", err)
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(postgresSchemaLock))
		if _, err := conn.ExecContext(ctx, postgres.Schema); err != nil {
			return fmt.Errorf("evals: init schema: %w", err)
		}
		return nil
	}
	return fmt.Errorf("unknown evals db backend %q", backend)
}
