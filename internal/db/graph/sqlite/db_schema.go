package sqlite

import (
	"database/sql"
	"fmt"

	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------

// The graph tables reference gb_datasets, which is created by the relational backend (internal/db/relational/sqlite),
// so that schema must be applied first.
// All DATETIME columns hold RFC 3339 UTC strings (SQLite has no real datetime type; see relational/sqlite/db_time.go).
const schema = `
-- An artifact is a piece of data ingested to be remembered: the raw input text plus when it happened. It is the
-- provenance of everything in the graph: entities and edges are anchored to the artifact(s) they were inferred from.
--   kind       'knowledge' (default: not strictly episodic) or 'episode' (a Graphiti-style episode, anchored in time)
--   valid_at   (event time): when the text was written/observed; relative dates in it resolve against it
--   created_at (system time): when it was ingested
CREATE TABLE IF NOT EXISTS gb_artifacts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id INTEGER NOT NULL REFERENCES gb_datasets(id),
	kind       TEXT NOT NULL DEFAULT 'knowledge',
	text       TEXT NOT NULL,
	valid_at   DATETIME NOT NULL,
	created_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS gb_entities (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id  INTEGER NOT NULL REFERENCES gb_datasets(id),
	name        TEXT NOT NULL,
	name_key    TEXT NOT NULL, -- normalized name used for dedup
	type        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	created_at  DATETIME NOT NULL DEFAULT (` + rsqlite.SQLNow + `),
	updated_at  DATETIME NOT NULL DEFAULT (` + rsqlite.SQLNow + `),
	UNIQUE (dataset_id, name_key, type)
);

-- An edge is a Graphiti entity edge: a fact (description) relating two entities, stored bi-temporally.
--   event time  (when the fact is true in the world): valid_at, invalid_at  (invalid_at NULL = still true)
--   system time (when we learned about it):           created_at, expired_at (expired_at NULL = not superseded)
-- artifact_id is the artifact that created the fact (see gb_edge_artifacts for every artifact that stated it). There is no uniqueness on (source, target, relation):
-- a fact can end and later become true again, which is a new row.
CREATE TABLE IF NOT EXISTS gb_edges (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id  INTEGER NOT NULL REFERENCES gb_datasets(id),
	source_id   INTEGER NOT NULL REFERENCES gb_entities(id) ON DELETE CASCADE,
	target_id   INTEGER NOT NULL REFERENCES gb_entities(id) ON DELETE CASCADE,
	relation    TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	valid_at    DATETIME NOT NULL,
	invalid_at  DATETIME,
	created_at  DATETIME NOT NULL,
	expired_at  DATETIME,
	artifact_id INTEGER REFERENCES gb_artifacts(id) ON DELETE SET NULL
);

-- Which artifacts an edge was inferred from (many-to-many).
--
-- gb_edges.artifact_id only records the artifact that CREATED the edge. When a later artifact states the same fact
-- again, the new edge is recognized as a duplicate and merged into the stored one instead of being inserted, which
-- would otherwise lose the fact that this artifact also supports it. This table records the creating artifact and
-- every artifact whose duplicate was merged into the edge, so a fact's full provenance (how many sources back it,
-- which texts to cite) is kept. The link is idempotent (primary key); deleting either side removes only the link.
CREATE TABLE IF NOT EXISTS gb_edge_artifacts (
	edge_id     INTEGER NOT NULL REFERENCES gb_edges(id) ON DELETE CASCADE,
	artifact_id INTEGER NOT NULL REFERENCES gb_artifacts(id) ON DELETE CASCADE,
	PRIMARY KEY (edge_id, artifact_id)
);

-- Which artifacts an entity was inferred from (many-to-many).
--
-- Why a link table rather than an artifact_id column on gb_entities: an entity is not owned by one artifact.
-- Entities are deduplicated per dataset on (name_key, type), so "Alice" mentioned in artifacts 1, 5 and 9 is ONE
-- row. A single artifact_id column could only say where she first appeared, and would lose every later mention,
-- so the graph could not answer "which artifacts is this entity grounded in?" or "which entities does this
-- artifact contribute to?" (needed for provenance, for re-processing or deleting an artifact without touching
-- entities that other artifacts also support, and for retrieving the source text around an entity). Edges do not
-- need this: each edge row is created by exactly one artifact (see gb_edges.artifact_id).
-- The link is idempotent (primary key), so repeated mentions inside one artifact add one row. Deleting either side
-- removes only the link.
CREATE TABLE IF NOT EXISTS gb_entity_artifacts (
	entity_id   INTEGER NOT NULL REFERENCES gb_entities(id) ON DELETE CASCADE,
	artifact_id INTEGER NOT NULL REFERENCES gb_artifacts(id) ON DELETE CASCADE,
	PRIMARY KEY (entity_id, artifact_id)
);

-- An ontology (metadata in gb_ontologies, see internal/db/relational) is a small graph of its own, kept apart from
-- the dataset graph above: classes are nodes, and edges are the relationships between classes.
--   kind: 'subclass_of' | 'equivalent_class' | 'disjoint_with' | 'property'
--   relation: edge name (the property's local name for 'property'); iri: the property or predicate IRI
--   label, comment, inverse_of: description of a property
CREATE TABLE IF NOT EXISTS gb_onto_classes (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	ontology_id INTEGER NOT NULL REFERENCES gb_ontologies(id) ON DELETE CASCADE,
	iri         TEXT NOT NULL,
	name        TEXT NOT NULL,
	label       TEXT NOT NULL DEFAULT '',
	comment     TEXT NOT NULL DEFAULT '',
	UNIQUE (ontology_id, iri)
);
CREATE TABLE IF NOT EXISTS gb_onto_edges (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	ontology_id INTEGER NOT NULL REFERENCES gb_ontologies(id) ON DELETE CASCADE,
	source_id   INTEGER NOT NULL REFERENCES gb_onto_classes(id) ON DELETE CASCADE,
	target_id   INTEGER NOT NULL REFERENCES gb_onto_classes(id) ON DELETE CASCADE,
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

CREATE INDEX IF NOT EXISTS idx_gb_artifacts_dataset ON gb_artifacts(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_entities_dataset ON gb_entities(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_dataset ON gb_edges(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_source ON gb_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_target ON gb_edges(target_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_artifact ON gb_edges(artifact_id);
CREATE INDEX IF NOT EXISTS idx_gb_edge_artifacts_artifact ON gb_edge_artifacts(artifact_id);
CREATE INDEX IF NOT EXISTS idx_gb_entity_artifacts_artifact ON gb_entity_artifacts(artifact_id);`

// initSchema applies the graph schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init graph schema: %w", err)
	}
	return nil
}
