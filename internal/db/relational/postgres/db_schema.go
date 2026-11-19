package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//---------------------------------------------------------------------------------------------

// The same tables as the sqlite backend (see sqlite/sql/schema.sql for what the columns mean), with native types:
// BIGSERIAL ids, TIMESTAMPTZ times, BOOLEAN flags and JSONB tags.
//
//go:embed sql/schema.sql
var schema string

// initSchema applies the database schema. Concurrent starters are serialized with an advisory lock, because
// CREATE ... IF NOT EXISTS can still race in PostgreSQL.
func initSchema(ctx context.Context,
	d *sql.DB) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	defer conn.Close()
	const lockID = 0x67620001 // arbitrary application-wide key
	if _, err := conn.ExecContext(ctx,
		`SELECT pg_advisory_lock($1)`,
		int64(lockID)); err != nil {
		return fmt.Errorf("init schema: lock: %w", err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx),
		`SELECT pg_advisory_unlock($1)`,
		int64(lockID))
	if err := dropOWLOntologies(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, schema+rememberRunsSchema+queryRunsSchema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}

// dropOWLOntologies removes the tables of the OWL ontologies (gb_ontologies with an "iri" column, gb_onto_classes, gb_onto_edges),
// which the YAML/JSON ontologies replace. The old ontologies cannot be converted: they are loaded again in the new format.
func dropOWLOntologies(ctx context.Context,
	conn *sql.Conn) error {
	var n int
	if err := conn.QueryRowContext(ctx,
		`
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'gb_ontologies' AND column_name = 'iri'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	for _, table := range []string{"gb_onto_edges", "gb_onto_classes", "gb_ontologies"} {
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS `+table+` CASCADE`); err != nil {
			return fmt.Errorf("drop %s: %w", table, err)
		}
	}
	return nil
}
