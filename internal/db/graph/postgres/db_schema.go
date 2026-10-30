package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	rpostgres "github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
)

//---------------------------------------------------------------------------------------------

// The same tables as the sqlite backend (see sqlite/db_schema.go for what the columns mean), with native types:
// BIGSERIAL ids and TIMESTAMPTZ times. They reference gb_datasets, which is created by the relational backend
// (internal/db/relational/postgres), so that schema must be applied first.
const schema = `
-- A timeseries (see sqlite/db_schema.go for what the columns mean). It comes first because gb_artifacts references it;
-- its points and anchors are defined further down.
CREATE TABLE IF NOT EXISTS gb_series (
	id          BIGSERIAL PRIMARY KEY,
	dataset_id  BIGINT NOT NULL REFERENCES gb_datasets(id),
	key         TEXT NOT NULL,
	field       TEXT NOT NULL DEFAULT 'value',
	name        TEXT NOT NULL DEFAULT '',
	unit        TEXT NOT NULL DEFAULT '',
	frequency   TEXT NOT NULL DEFAULT '',
	source      TEXT NOT NULL DEFAULT 'supplied',
	provider    TEXT NOT NULL DEFAULT '',
	definition  TEXT NOT NULL DEFAULT '',
	first_ts    TIMESTAMPTZ,
	last_ts     TIMESTAMPTZ,
	point_count BIGINT NOT NULL DEFAULT 0,
	stats       TEXT NOT NULL DEFAULT '',
	created_at  TIMESTAMPTZ NOT NULL,
	updated_at  TIMESTAMPTZ NOT NULL,
	UNIQUE (dataset_id, provider, key, field)
);

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
	-- Only for a 'web_search' artifact: the query that was issued; its parent_id is the artifact the searched entity
	-- came from (see sqlite/db_schema.go).
	query        TEXT,
	-- Only for a 'chunk_summary' artifact: the run of the model that wrote it (see sqlite/db_schema.go).
	inference_id BIGINT REFERENCES gb_inferences(id),
	-- Only for a 'series_observation' artifact: the series it describes (see sqlite/db_schema.go).
	series_id    BIGINT REFERENCES gb_series(id) ON DELETE SET NULL,
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

-- What each Remember call did to the temporal state of the facts (see sqlite/db_schema.go for what the columns mean).
CREATE TABLE IF NOT EXISTS gb_edge_events (
	id                BIGSERIAL PRIMARY KEY,
	dataset_id        BIGINT NOT NULL REFERENCES gb_datasets(id),
	edge_id           BIGINT NOT NULL REFERENCES gb_edges(id) ON DELETE CASCADE,
	artifact_id       BIGINT REFERENCES gb_artifacts(id) ON DELETE SET NULL,
	event             TEXT NOT NULL,
	invalid_at_before TIMESTAMPTZ,
	invalid_at_after  TIMESTAMPTZ,
	caused_by_edge_id BIGINT REFERENCES gb_edges(id) ON DELETE SET NULL,
	created_at        TIMESTAMPTZ NOT NULL
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

CREATE TABLE IF NOT EXISTS gb_series_points (
	series_id BIGINT NOT NULL REFERENCES gb_series(id) ON DELETE CASCADE,
	ts        TIMESTAMPTZ NOT NULL,
	value     DOUBLE PRECISION NOT NULL,
	PRIMARY KEY (series_id, ts)
);
CREATE TABLE IF NOT EXISTS gb_series_anchors (
	id           BIGSERIAL PRIMARY KEY,
	series_id    BIGINT NOT NULL REFERENCES gb_series(id) ON DELETE CASCADE,
	artifact_id  BIGINT REFERENCES gb_artifacts(id) ON DELETE CASCADE,
	entity_id    BIGINT REFERENCES gb_entities(id) ON DELETE CASCADE,
	edge_id      BIGINT REFERENCES gb_edges(id) ON DELETE CASCADE,
	role         TEXT NOT NULL DEFAULT '',
	window_start TIMESTAMPTZ,
	window_end   TIMESTAMPTZ,
	created_at   TIMESTAMPTZ NOT NULL,
	CHECK (num_nonnulls(artifact_id, entity_id, edge_id) = 1)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_series_anchors_artifact ON gb_series_anchors(series_id, artifact_id) WHERE artifact_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_series_anchors_entity ON gb_series_anchors(series_id, entity_id) WHERE entity_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_series_anchors_edge ON gb_series_anchors(series_id, edge_id) WHERE edge_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_gb_series_dataset ON gb_series(dataset_id);

-- The geo metadata of the entities that are places (see sqlite/db_schema.go for what the columns mean).
CREATE TABLE IF NOT EXISTS gb_entity_geo (
	entity_id    BIGINT PRIMARY KEY REFERENCES gb_entities(id) ON DELETE CASCADE,
	kind         TEXT NOT NULL,
	query        TEXT NOT NULL DEFAULT '',
	latitude     DOUBLE PRECISION NOT NULL CHECK (latitude BETWEEN -90 AND 90),
	longitude    DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
	display_name TEXT NOT NULL DEFAULT '',
	country      TEXT NOT NULL DEFAULT '',
	country_code TEXT NOT NULL DEFAULT '',
	region       TEXT NOT NULL DEFAULT '',
	place_type   TEXT NOT NULL DEFAULT '',
	bbox_south   DOUBLE PRECISION,
	bbox_west    DOUBLE PRECISION,
	bbox_north   DOUBLE PRECISION,
	bbox_east    DOUBLE PRECISION,
	importance   DOUBLE PRECISION NOT NULL DEFAULT 0,
	provider     TEXT NOT NULL,
	provider_ref TEXT NOT NULL DEFAULT '',
	created_at   TIMESTAMPTZ NOT NULL,
	updated_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_gb_entity_geo_coords ON gb_entity_geo(latitude, longitude);
CREATE INDEX IF NOT EXISTS idx_gb_entity_geo_country ON gb_entity_geo(country_code);


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
CREATE INDEX IF NOT EXISTS idx_gb_edge_events_artifact ON gb_edge_events(artifact_id);
CREATE INDEX IF NOT EXISTS idx_gb_edge_events_edge ON gb_edge_events(edge_id);
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

// geoIndexSQL is the PostGIS geo index of gb_entity_geo, as a format with the schema of the extension (%[1]s, quoted; it may not
// be on the search path): a generated geography point (so it follows the latitude and the longitude of the row however it is
// written, and is filled in for the rows stored before the column existed), and a GiST index on it, which proximity queries use
// (ST_DWithin(geo, ST_MakePoint(lon, lat)::geography, meters)).
const geoIndexSQL = `
ALTER TABLE gb_entity_geo ADD COLUMN IF NOT EXISTS geo %[1]s.geography(Point, 4326)
	GENERATED ALWAYS AS (%[1]s.ST_SetSRID(%[1]s.ST_MakePoint(longitude, latitude), 4326)::%[1]s.geography) STORED;
CREATE INDEX IF NOT EXISTS idx_gb_entity_geo_gist ON gb_entity_geo USING GIST (geo);`

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
