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
-- A memory is a Graphiti "episode": the raw input text plus when it happened.
--   valid_at   (event time): when the text was written/observed; relative dates in it resolve against it
--   created_at (system time): when it was ingested
CREATE TABLE IF NOT EXISTS gb_memories (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id INTEGER NOT NULL REFERENCES gb_datasets(id),
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
-- memory_id is the episode the fact was extracted from. There is no uniqueness on (source, target, relation):
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
	memory_id   INTEGER REFERENCES gb_memories(id) ON DELETE SET NULL
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

CREATE INDEX IF NOT EXISTS idx_gb_memories_dataset ON gb_memories(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_entities_dataset ON gb_entities(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_dataset ON gb_edges(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_source ON gb_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_target ON gb_edges(target_id);`

// initSchema applies the graph schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init graph schema: %w", err)
	}
	return nil
}
