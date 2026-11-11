package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
)

//---------------------------------------------------------------------------------------------

// The same tables as the sqlite backend (see sqlite/sql/schema.sql for what the columns mean), with native types:
// BIGSERIAL ids and TIMESTAMPTZ times. They reference gb_datasets, which is created by the relational backend
// (internal/db/relational/postgres), so that schema must be applied first.
//
//go:embed sql/schema.sql
var schema string

// initSchema applies the graph schema. Concurrent starters are serialized with an advisory lock, because
// CREATE ... IF NOT EXISTS can still race in PostgreSQL.
func initSchema(ctx context.Context, d *sql.DB) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("init graph schema: %w", err)
	}
	defer conn.Close()
	const lockID = 0x67620002 // arbitrary application-wide key (the relational schema uses 0x67620001)
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(lockID)); err != nil {
		return fmt.Errorf("init graph schema: lock: %w", err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(lockID))
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("init graph schema: %w", err)
	}
	return nil
}

// geoIndexSQL is the PostGIS geo index of gb_entity_geo, as a format with the schema of the extension (%[1]s, quoted; it may not
// be on the search path): a generated geography point (so it follows the latitude and the longitude of the row however it is
// written, and is filled in for the rows stored before the column existed), and a GiST index on it, which proximity queries use
// (ST_DWithin(geo, ST_MakePoint(lon, lat)::geography, meters)).
//
//go:embed sql/optional_geo_index.sql
var geoIndexSQL string

// EnableGeoIndex adds the PostGIS geo index to gb_entity_geo; it is only called when geo is on. The result says whether the
// index exists: false when the PostGIS extension is not enabled in the database (the places are stored all the same, without
// an index, and the admin UI reports it; the next start with geo on after `CREATE EXTENSION postgis` creates it). The extension
// is never created here. The generated column fills itself in for the rows stored before (PostgreSQL has no way to add it
// without), so unlike for SQLite those are indexed too.
func EnableGeoIndex(ctx context.Context, d *sql.DB) (bool, error) {
	conn, err := d.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("init geo index: %w", err)
	}
	defer conn.Close()
	const lockID = 0x67620002 // the same lock as initSchema: the table must exist and nobody else may alter it meanwhile
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(lockID)); err != nil {
		return false, fmt.Errorf("init geo index: lock: %w", err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(lockID))
	var ns string
	err = conn.QueryRowContext(ctx, `SELECT quote_ident(n.nspname) FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'postgis'`).Scan(&ns)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("init geo index: look for PostGIS: %w", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(geoIndexSQL, ns)); err != nil {
		return false, fmt.Errorf("init geo index: %w", err)
	}
	return true, nil
}
