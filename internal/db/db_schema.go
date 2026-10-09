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

CREATE TABLE IF NOT EXISTS gb_memories (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	text       TEXT NOT NULL,
	reference_time TEXT, -- RFC 3339 UTC: "now" for relative dates in the text (Graphiti's episode valid_at)
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS gb_entities (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL,
	name_key    TEXT NOT NULL, -- normalized name used for dedup
	type        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (name_key, type)
);

CREATE TABLE IF NOT EXISTS gb_edges (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	source_id   INTEGER NOT NULL REFERENCES gb_entities(id) ON DELETE CASCADE,
	target_id   INTEGER NOT NULL REFERENCES gb_entities(id) ON DELETE CASCADE,
	relation    TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	memory_id   INTEGER REFERENCES gb_memories(id) ON DELETE SET NULL,
	created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (source_id, target_id, relation)
);
CREATE INDEX IF NOT EXISTS idx_gb_edges_target ON gb_edges(target_id);

-- Events follow Graphiti's bi-temporal model. All timestamps are RFC 3339 UTC strings.
--   event time  (when it is true in the world): valid_at, invalid_at  (NULL = unknown / still true)
--   system time (when we learned about it):     created_at, expired_at (NULL = not superseded)
CREATE TABLE IF NOT EXISTS gb_events (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	name            TEXT NOT NULL,
	description     TEXT NOT NULL,
	description_key TEXT NOT NULL, -- normalized description used for dedup
	valid_at        TEXT,
	invalid_at      TEXT,
	created_at      TEXT NOT NULL,
	expired_at      TEXT,
	memory_id       INTEGER REFERENCES gb_memories(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS gb_event_participants (
	event_id  INTEGER NOT NULL REFERENCES gb_events(id) ON DELETE CASCADE,
	entity_id INTEGER NOT NULL REFERENCES gb_entities(id) ON DELETE CASCADE,
	PRIMARY KEY (event_id, entity_id)
);
CREATE INDEX IF NOT EXISTS idx_gb_event_participants_entity ON gb_event_participants(entity_id);`

//---------------------------------------------------------------------------------------------

// initSchema applies the database schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
