package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

//---------------------------------------------------------------------------------------------

// The same tables as the sqlite backend (see sqlite/db_schema.go for what the columns mean), with native types:
// BIGSERIAL ids, TIMESTAMPTZ times, BOOLEAN flags and JSONB tags.
const schema = `
CREATE TABLE IF NOT EXISTS gb_items (
	id         BIGSERIAL PRIMARY KEY,
	name       TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT ` + SQLNow + `
);

CREATE TABLE IF NOT EXISTS gb_datasets (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	tags        JSONB NOT NULL DEFAULT '[]',
	created_at  TIMESTAMPTZ NOT NULL,
	deleted     BOOLEAN NOT NULL DEFAULT FALSE,
	deleted_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_datasets_user_name ON gb_datasets(user_id, name) WHERE NOT deleted;

CREATE TABLE IF NOT EXISTS gb_llm_calls (
	id            BIGSERIAL PRIMARY KEY,
	type          TEXT NOT NULL,
	provider      TEXT NOT NULL,
	model         TEXT NOT NULL,
	input_tokens  BIGINT NOT NULL,
	output_tokens BIGINT NOT NULL,
	cost_usd      DOUBLE PRECISION NOT NULL, -- total cost of the call in USD (0 if the model's price is unknown)
	created_at    TIMESTAMPTZ NOT NULL DEFAULT ` + SQLNow + `
);
CREATE INDEX IF NOT EXISTS idx_gb_llm_calls_type ON gb_llm_calls(type);`

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
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
