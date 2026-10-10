package postgres

import (
	"context"
	"database/sql"
	"fmt"

	rpostgres "github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
)

//---------------------------------------------------------------------------------------------

// The same tables as the sqlite backend (see sqlite/db_schema.go for what the columns mean), with native types:
// BIGSERIAL ids and TIMESTAMPTZ times. They reference gb_datasets, which is created by the relational backend
// (internal/db/relational/postgres), so that schema must be applied first.
const schema = `
CREATE TABLE IF NOT EXISTS gb_memories (
	id         BIGSERIAL PRIMARY KEY,
	dataset_id BIGINT NOT NULL REFERENCES gb_datasets(id),
	text       TEXT NOT NULL,
	valid_at   TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS gb_entities (
	id          BIGSERIAL PRIMARY KEY,
	dataset_id  BIGINT NOT NULL REFERENCES gb_datasets(id),
	name        TEXT NOT NULL,
	name_key    TEXT NOT NULL, -- normalized name used for dedup
	type        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	created_at  TIMESTAMPTZ NOT NULL DEFAULT ` + rpostgres.SQLNow + `,
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT ` + rpostgres.SQLNow + `,
	UNIQUE (dataset_id, name_key, type)
);

CREATE TABLE IF NOT EXISTS gb_edges (
	id          BIGSERIAL PRIMARY KEY,
	dataset_id  BIGINT NOT NULL REFERENCES gb_datasets(id),
	source_id   BIGINT NOT NULL REFERENCES gb_entities(id) ON DELETE CASCADE,
	target_id   BIGINT NOT NULL REFERENCES gb_entities(id) ON DELETE CASCADE,
	relation    TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	valid_at    TIMESTAMPTZ NOT NULL,
	invalid_at  TIMESTAMPTZ,
	created_at  TIMESTAMPTZ NOT NULL,
	expired_at  TIMESTAMPTZ,
	memory_id   BIGINT REFERENCES gb_memories(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS gb_onto_classes (
	id          BIGSERIAL PRIMARY KEY,
	ontology_id BIGINT NOT NULL REFERENCES gb_ontologies(id) ON DELETE CASCADE,
	iri         TEXT NOT NULL,
	name        TEXT NOT NULL,
	label       TEXT NOT NULL DEFAULT '',
	comment     TEXT NOT NULL DEFAULT '',
	UNIQUE (ontology_id, iri)
);
CREATE TABLE IF NOT EXISTS gb_onto_edges (
	id          BIGSERIAL PRIMARY KEY,
	ontology_id BIGINT NOT NULL REFERENCES gb_ontologies(id) ON DELETE CASCADE,
	source_id   BIGINT NOT NULL REFERENCES gb_onto_classes(id) ON DELETE CASCADE,
	target_id   BIGINT NOT NULL REFERENCES gb_onto_classes(id) ON DELETE CASCADE,
	relation    TEXT NOT NULL,
	kind        TEXT NOT NULL,
	iri         TEXT NOT NULL,
	label       TEXT NOT NULL DEFAULT '',
	comment     TEXT NOT NULL DEFAULT '',
	inverse_of  TEXT NOT NULL DEFAULT '',
	UNIQUE (ontology_id, source_id, target_id, kind, iri)
);
CREATE INDEX IF NOT EXISTS idx_gb_onto_edges_source ON gb_onto_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_gb_onto_edges_target ON gb_onto_edges(target_id);

CREATE INDEX IF NOT EXISTS idx_gb_memories_dataset ON gb_memories(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_entities_dataset ON gb_entities(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_dataset ON gb_edges(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_source ON gb_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_target ON gb_edges(target_id);`

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
