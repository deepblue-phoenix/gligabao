// Package db connects the GBerror (package errors) to the databases: Open applies the schema, Sink gives the errors.Sink that
// stores into the chosen backend. The backends are the subpackages sqlite and postgres of package errors.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	gberrors "github.com/deepblue-phoenix/gligabao/internal/sys/errors"
	"github.com/deepblue-phoenix/gligabao/internal/sys/errors/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/sys/errors/sqlite"
)

// postgresSchemaLock is the advisory lock key that serializes the starters that apply the schema.
const postgresSchemaLock = 0x67620008

// Open applies the schema of the table gb_errors to d, the relational database opened for the backend (config.DBBackendSQLite or
// config.DBBackendPostgres; empty is sqlite). The statements are idempotent. Its signature is that of db.Schema.
func Open(backend string, d *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch backend {
	case "", config.DBBackendSQLite:
		if _, err := d.ExecContext(ctx, sqlite.Schema); err != nil {
			return fmt.Errorf("errors: init schema: %w", err)
		}
		return nil
	case config.DBBackendPostgres:
		conn, err := d.Conn(ctx)
		if err != nil {
			return fmt.Errorf("errors: init schema: %w", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(postgresSchemaLock)); err != nil {
			return fmt.Errorf("errors: init schema: lock: %w", err)
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(postgresSchemaLock))
		if _, err := conn.ExecContext(ctx, postgres.Schema); err != nil {
			return fmt.Errorf("errors: init schema: %w", err)
		}
		return nil
	}
	return fmt.Errorf("unknown errors db backend %q", backend)
}

// Sink returns the errors.Sink that inserts into gb_errors of d, for the backend (as in Open).
func Sink(backend string, d gberrors.Execer) (gberrors.Sink, error) {
	switch backend {
	case "", config.DBBackendSQLite:
		return func(ctx context.Context, e *gberrors.GBerror) error { return sqlite.Insert(ctx, d, e) }, nil
	case config.DBBackendPostgres:
		return func(ctx context.Context, e *gberrors.GBerror) error { return postgres.Insert(ctx, d, e) }, nil
	}
	return nil, fmt.Errorf("unknown errors db backend %q", backend)
}
