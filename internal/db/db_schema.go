package db

import (
	"database/sql"
	"fmt"
)

//---------------------------------------------------------------------------------------------

const schema = `
CREATE TABLE IF NOT EXISTS gb_items (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- A dataset is a hard logical container: every memory, entity and edge belongs to exactly one dataset, and
-- datasets never share nodes. A dataset is owned by one user (user_id); everything in it belongs to that user.
-- Names are unique per user. Deletion is soft (deleted = 1); the rows stay but are invisible, and the name can
-- be reused by a new dataset (with a new id and an empty graph).
--   tags: JSON array of strings; created_at / deleted_at: RFC 3339 UTC
CREATE TABLE IF NOT EXISTS gb_datasets (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	tags        TEXT NOT NULL DEFAULT '[]',
	created_at  TEXT NOT NULL,
	deleted     INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
	deleted_at  TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_datasets_user_name ON gb_datasets(user_id, name) WHERE deleted = 0;

-- A memory is a Graphiti "episode": the raw input text plus when it happened.
--   valid_at   (event time): RFC 3339 UTC, when the text was written/observed; relative dates in it resolve against it
--   created_at (system time): when it was ingested
CREATE TABLE IF NOT EXISTS gb_memories (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id INTEGER NOT NULL REFERENCES gb_datasets(id),
	text       TEXT NOT NULL,
	valid_at   TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS gb_entities (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id  INTEGER NOT NULL REFERENCES gb_datasets(id),
	name        TEXT NOT NULL,
	name_key    TEXT NOT NULL, -- normalized name used for dedup
	type        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (dataset_id, name_key, type)
);

-- An edge is a Graphiti entity edge: a fact (description) relating two entities, stored bi-temporally.
-- All timestamps are RFC 3339 UTC strings.
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
	valid_at    TEXT NOT NULL,
	invalid_at  TEXT,
	created_at  TEXT NOT NULL,
	expired_at  TEXT,
	memory_id   INTEGER REFERENCES gb_memories(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_gb_memories_dataset ON gb_memories(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_entities_dataset ON gb_entities(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_dataset ON gb_edges(dataset_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_source ON gb_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_gb_edges_target ON gb_edges(target_id);

-- One row per LLM/embedding API call. type is the operation that made the call: 'remember' or 'recall'.
CREATE TABLE IF NOT EXISTS gb_llm_calls (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	type          TEXT NOT NULL,
	provider      TEXT NOT NULL,
	model         TEXT NOT NULL,
	input_tokens  INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cost_usd      REAL NOT NULL, -- total cost of the call in USD (0 if the model's price is unknown)
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_gb_llm_calls_type ON gb_llm_calls(type);`

//---------------------------------------------------------------------------------------------

// DefaultDataset is the dataset used when a request names none. It is created per user on first remember.
const DefaultDataset = "main"

// initSchema applies the database schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
