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
CREATE TABLE IF NOT EXISTS gb_artifacts (
	id         BIGSERIAL PRIMARY KEY,
	dataset_id BIGINT NOT NULL REFERENCES gb_datasets(id),
	kind       TEXT NOT NULL DEFAULT 'knowledge',
	text       TEXT NOT NULL,
	session_id TEXT, -- the session that remembered it; NULL if none (see sqlite/db_schema.go)
	-- A chunk of a long text is an artifact whose parent is the artifact holding the whole text; the columns are NULL
	-- for any other artifact (see sqlite/db_schema.go).
	parent_id    BIGINT REFERENCES gb_artifacts(id) ON DELETE CASCADE,
	chunk_index  INTEGER,
	start_offset INTEGER,
	end_offset   INTEGER,
	tokens       INTEGER,
	valid_at   TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL,
	UNIQUE (parent_id, chunk_index)
);

CREATE TABLE IF NOT EXISTS gb_entities (
	id          BIGSERIAL PRIMARY KEY,
	dataset_id  BIGINT NOT NULL REFERENCES gb_datasets(id),
	name        TEXT NOT NULL,
	name_key    TEXT NOT NULL, -- normalized name used for dedup
	type        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	model_id    TEXT REFERENCES gb_models(id), -- the model that inferred the entity (see sqlite/db_schema.go)
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
	artifact_id BIGINT REFERENCES gb_artifacts(id) ON DELETE SET NULL,
	model_id    TEXT REFERENCES gb_models(id) -- the model that inferred the fact
);

-- Edge <-> artifact provenance, many-to-many: gb_edges.artifact_id is only the artifact that created the edge; this
-- also records every later artifact whose duplicate of the fact was merged into it (see sqlite/db_schema.go).
CREATE TABLE IF NOT EXISTS gb_edge_artifacts (
	edge_id     BIGINT NOT NULL REFERENCES gb_edges(id) ON DELETE CASCADE,
	artifact_id BIGINT NOT NULL REFERENCES gb_artifacts(id) ON DELETE CASCADE,
	PRIMARY KEY (edge_id, artifact_id)
);

-- Entity <-> artifact provenance, many-to-many: an entity is deduplicated per dataset and so is inferred from many
-- artifacts, which one artifact_id column could not express (see sqlite/db_schema.go for the full rationale).
CREATE TABLE IF NOT EXISTS gb_entity_artifacts (
	entity_id   BIGINT NOT NULL REFERENCES gb_entities(id) ON DELETE CASCADE,
	artifact_id BIGINT NOT NULL REFERENCES gb_artifacts(id) ON DELETE CASCADE,
	PRIMARY KEY (entity_id, artifact_id)
);

-- Which sessions an entity / an edge was discovered in, many-to-many (see sqlite/db_schema.go): recorded for every
-- artifact of a session that mentions the entity or states the fact, including duplicates of stored ones.
CREATE TABLE IF NOT EXISTS gb_entity_sessions (
	entity_id  BIGINT NOT NULL REFERENCES gb_entities(id) ON DELETE CASCADE,
	session_id TEXT NOT NULL,
	PRIMARY KEY (entity_id, session_id)
);

CREATE TABLE IF NOT EXISTS gb_edge_sessions (
	edge_id    BIGINT NOT NULL REFERENCES gb_edges(id) ON DELETE CASCADE,
	session_id TEXT NOT NULL,
	PRIMARY KEY (edge_id, session_id)
);

-- The image pipeline (see sqlite/db_schema.go for what the columns mean).
CREATE TABLE IF NOT EXISTS gb_image_artifacts (
	id         BIGSERIAL PRIMARY KEY,
	dataset_id BIGINT NOT NULL REFERENCES gb_datasets(id),
	session_id TEXT, -- the session that remembered it; NULL if none
	media_type TEXT NOT NULL,
	sha256     TEXT NOT NULL,
	size_bytes BIGINT NOT NULL,
	path       TEXT NOT NULL,
	valid_at   TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS gb_image_extractions (
	id                BIGSERIAL PRIMARY KEY,
	image_artifact_id BIGINT NOT NULL REFERENCES gb_image_artifacts(id) ON DELETE CASCADE,
	provider          TEXT NOT NULL,
	model             TEXT NOT NULL,
	theme             TEXT NOT NULL,
	title             TEXT NOT NULL DEFAULT '',
	text              TEXT NOT NULL DEFAULT '',
	raw_output        TEXT NOT NULL DEFAULT '',
	input_tokens      INTEGER NOT NULL DEFAULT 0,
	output_tokens     INTEGER NOT NULL DEFAULT 0,
	cost_usd          DOUBLE PRECISION NOT NULL DEFAULT 0,
	created_at        TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS gb_image_text_blocks (
	id            BIGSERIAL PRIMARY KEY,
	extraction_id BIGINT NOT NULL REFERENCES gb_image_extractions(id) ON DELETE CASCADE,
	position      INTEGER NOT NULL,
	kind          TEXT NOT NULL,
	text          TEXT NOT NULL,
	UNIQUE (extraction_id, position)
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

CREATE INDEX IF NOT EXISTS idx_gb_image_artifacts_dataset ON gb_image_artifacts(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_image_extractions_image ON gb_image_extractions(image_artifact_id);
CREATE INDEX IF NOT EXISTS idx_gb_artifacts_dataset ON gb_artifacts(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_artifacts_parent ON gb_artifacts(parent_id);
CREATE INDEX IF NOT EXISTS idx_gb_entities_dataset ON gb_entities(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_dataset ON gb_edges(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_source ON gb_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_target ON gb_edges(target_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_artifact ON gb_edges(artifact_id);
CREATE INDEX IF NOT EXISTS idx_gb_edge_artifacts_artifact ON gb_edge_artifacts(artifact_id);
CREATE INDEX IF NOT EXISTS idx_gb_entity_artifacts_artifact ON gb_entity_artifacts(artifact_id);
CREATE INDEX IF NOT EXISTS idx_gb_artifacts_session ON gb_artifacts(dataset_id, session_id);
CREATE INDEX IF NOT EXISTS idx_gb_image_artifacts_session ON gb_image_artifacts(dataset_id, session_id);
CREATE INDEX IF NOT EXISTS idx_gb_entity_sessions_session ON gb_entity_sessions(session_id);
CREATE INDEX IF NOT EXISTS idx_gb_edge_sessions_session ON gb_edge_sessions(session_id);`

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
