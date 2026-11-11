package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//---------------------------------------------------------------------------------------------

// The same tables as the sqlite backend (see sqlite/sql/schema.sql for what the columns mean), with native types.
// They are created in the same database as the relational data, which is opened by internal/db/relational/postgres.
//
//go:embed sql/schema.sql
var schema string

// initSchema applies the training schema. Concurrent starters are serialized with an advisory lock, because
// CREATE ... IF NOT EXISTS can still race in PostgreSQL.
func initSchema(ctx context.Context, d *sql.DB) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("init training schema: %w", err)
	}
	defer conn.Close()
	const lockID = 0x67620004 // arbitrary application-wide key (relational uses 0x67620001, graph 0x67620002, evals 0x67620003)
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(lockID)); err != nil {
		return fmt.Errorf("init training schema: lock: %w", err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(lockID))
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("init training schema: %w", err)
	}
	return nil
}
