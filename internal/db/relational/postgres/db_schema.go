package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//---------------------------------------------------------------------------------------------

// The same tables as the sqlite backend (see sqlite/schema.sql for what the columns mean), with native types:
// BIGSERIAL ids, TIMESTAMPTZ times, BOOLEAN flags and JSONB tags.
//
//go:embed schema.sql
var schema string

// initSchema applies the database schema. Concurrent starters are serialized with an advisory lock, because
// CREATE ... IF NOT EXISTS can still race in PostgreSQL.
func initSchema(ctx context.Context, d *sql.DB) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	defer conn.Close()
	const lockID = 0x67620001 // arbitrary application-wide key
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(lockID)); err != nil {
		return fmt.Errorf("init schema: lock: %w", err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(lockID))
	if _, err := conn.ExecContext(ctx, schema+rememberRunsSchema+queryRunsSchema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
