package sqlite

import (
	"database/sql"
	"fmt"
)

//---------------------------------------------------------------------------------------------

const schema = `
CREATE TABLE IF NOT EXISTS gb_items (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	created_at DATETIME NOT NULL DEFAULT (` + SQLNow + `)
);

-- A dataset is a hard logical container: every memory, entity and edge (see internal/db/graph) belongs to exactly
-- one dataset, and datasets never share nodes. A dataset is owned by one user (user_id); everything in it belongs to that user.
-- Names are unique per user. Deletion is soft (deleted = 1); the rows stay but are invisible, and the name can
-- be reused by a new dataset (with a new id and an empty graph).
--   tags: JSON array of strings
-- All DATETIME columns hold RFC 3339 UTC strings (SQLite has no real datetime type; see db_time.go).
CREATE TABLE IF NOT EXISTS gb_datasets (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	tags        TEXT NOT NULL DEFAULT '[]',
	created_at  DATETIME NOT NULL,
	deleted     INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
	deleted_at  DATETIME
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_datasets_user_name
	ON gb_datasets(user_id, name)
	WHERE deleted = 0;

-- Metadata of a loaded ontology. The ontology itself (classes and relationships) is in the graph tables
-- gb_onto_classes and gb_onto_edges (internal/db/graph), which reference this row. Names are unique per user.
--   sha256: hex digest of the loaded document; classes/properties/edges: what was stored in the graph
CREATE TABLE IF NOT EXISTS gb_ontologies (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	iri         TEXT NOT NULL DEFAULT '',
	title       TEXT NOT NULL DEFAULT '',
	format      TEXT NOT NULL,
	size_bytes  INTEGER NOT NULL,
	sha256      TEXT NOT NULL,
	classes     INTEGER NOT NULL,
	properties  INTEGER NOT NULL,
	edges       INTEGER NOT NULL,
	created_at  DATETIME NOT NULL,
	updated_at  DATETIME NOT NULL,
	UNIQUE (user_id, name)
);

-- One row per LLM/embedding API call. type is the operation that made the call: 'remember' or 'recall'.
CREATE TABLE IF NOT EXISTS gb_llm_calls (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	type          TEXT NOT NULL,
	provider      TEXT NOT NULL,
	model         TEXT NOT NULL,
	input_tokens  INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cost_usd      REAL NOT NULL, -- total cost of the call in USD (0 if the model's price is unknown)
	created_at    DATETIME NOT NULL DEFAULT (` + SQLNow + `)
);
CREATE INDEX IF NOT EXISTS idx_gb_llm_calls_type ON gb_llm_calls(type);`

// initSchema applies the database schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
