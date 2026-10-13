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
--   kind       'knowledge' (default: not strictly episodic) or 'episode' (a Graphiti-style episode, anchored in time),
--              or 'web_search': the result of a web search that remember made for an entity of another artifact (see
--              parent_id and query). Only remember creates those; a caller cannot ask for that kind.
--   valid_at   (event time): when the text was written/observed; relative dates in it resolve against it
--   created_at (system time): when it was ingested
--   session_id the user/agent session that remembered it (a caller-chosen string); NULL if none was given. It is
--              what links artifacts from the same session; see gb_entity_sessions and gb_edge_sessions for the
--              entities and edges.
--   parent_id  NULL for an artifact the user sent (a note, or a long text/document, which holds the whole text). The
--              chunks of a long text are artifacts too: children of it, with parent_id set. They have the kind, session
--              and times of the parent, and their own text, which is text[start_offset:end_offset] of the parent's
--              (bytes; overlap with the previous chunk included). Chunks are what the entity/edge extraction runs on,
--              one by one, so the entities and edges of a long text link to its chunks, not to the parent.
--              Deleting the parent deletes its chunks.
--              A 'web_search' artifact has a parent_id too: the artifact FROM WHICH THE SEARCHED ENTITY CAME (the text, or
--              for a long text the chunk, that the entity was extracted from). So an artifact's children are its chunks
--              and the web searches made for its entities, told apart by kind ('web_search' or not) and by chunk_index,
--              which is NULL for a web search. It has the session of the remember call, its own times (valid_at is
--              when the search was made, not the time of the parent), and the entities and edges extracted from the
--              search result link to it, like to any artifact.
--   query      only for a 'web_search' artifact: the web search query that was issued (what the artifact is the result
--              of); NULL for any other artifact.
--   chunk_index position of a chunk among its siblings, from 0; tokens its size in tokens. NULL unless a chunk.
CREATE TABLE IF NOT EXISTS gb_artifacts (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id   INTEGER NOT NULL REFERENCES gb_datasets(id),
	kind         TEXT NOT NULL DEFAULT 'knowledge',
	text         TEXT NOT NULL,
	session_id   TEXT,
	parent_id    INTEGER REFERENCES gb_artifacts(id) ON DELETE CASCADE,
	chunk_index  INTEGER,
	start_offset INTEGER,
	end_offset   INTEGER,
	tokens       INTEGER,
	query        TEXT,
	valid_at     DATETIME NOT NULL,
	created_at   DATETIME NOT NULL,
	UNIQUE (parent_id, chunk_index)
);

-- model_id (entities and edges): the model that made the inference, a reference to gb_models (see
-- internal/db/relational); NULL if unknown. It is the model the LLM API reported for the extraction call. Entities are
-- shared between artifacts, so an entity's model_id is that of the model that wrote its stored state: the one that
-- created it, then whichever last replaced its description (a re-mention without a description leaves it alone).
CREATE TABLE IF NOT EXISTS gb_entities (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id  INTEGER NOT NULL REFERENCES gb_datasets(id),
	name        TEXT NOT NULL,
	name_key    TEXT NOT NULL, -- normalized name used for dedup
	type        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	model_id    TEXT REFERENCES gb_models(id), -- the model that inferred the entity (see the comment above)
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
	artifact_id INTEGER REFERENCES gb_artifacts(id) ON DELETE SET NULL,
	model_id    TEXT REFERENCES gb_models(id) -- the model that inferred the fact
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

-- Which sessions an entity was discovered in (many-to-many).
--
-- gb_entity_artifacts plus gb_artifacts.session_id would give the same answer only while the artifact rows exist,
-- and only through a join. This table records the session directly, whenever an artifact of a session mentions the
-- entity: when the entity is created, and also when it already existed and the mention was therefore a duplicate that
-- writes no new entity row. Without it, the second session that mentions "Alice" would leave no trace of the fact
-- that it saw her. session_id is the caller's string (not a reference: sessions have no table of their own). The
-- link is idempotent (primary key); deleting the entity removes its links. Artifacts without a session add no row.
CREATE TABLE IF NOT EXISTS gb_entity_sessions (
	entity_id  INTEGER NOT NULL REFERENCES gb_entities(id) ON DELETE CASCADE,
	session_id TEXT NOT NULL,
	PRIMARY KEY (entity_id, session_id)
);

-- Which sessions an edge (a fact) was discovered in (many-to-many): the session of the artifact that created it and
-- of every later artifact whose duplicate of the fact was merged into it. The edge counterpart of gb_entity_sessions.
CREATE TABLE IF NOT EXISTS gb_edge_sessions (
	edge_id    INTEGER NOT NULL REFERENCES gb_edges(id) ON DELETE CASCADE,
	session_id TEXT NOT NULL,
	PRIMARY KEY (edge_id, session_id)
);

-- The image pipeline (see internal/memory/memory_image/image.go), kept apart from gb_artifacts and the graph.
--
-- An image artifact is an image ingested to be remembered. The table only tracks it: its media type, SHA-256 and
-- size, and path, the file's location relative to the image directory (the bytes live on the file system, never in
-- the database; see internal/memory/memory_image/image_store.go). valid_at is its event time (when it was captured/observed), created_at
-- when it was ingested.
CREATE TABLE IF NOT EXISTS gb_image_artifacts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	dataset_id INTEGER NOT NULL REFERENCES gb_datasets(id),
	session_id TEXT, -- the session that remembered it; NULL if none (see gb_artifacts)
	media_type TEXT NOT NULL,
	sha256     TEXT NOT NULL,
	size_bytes INTEGER NOT NULL,
	path       TEXT NOT NULL,
	valid_at   DATETIME NOT NULL,
	created_at DATETIME NOT NULL
);

-- What an LLM read from an image artifact (several extractions per image are possible, e.g. from different models).
--   theme  what the whole image is: graph, plot, chart, diagram, table, document, screenshot, slide, map, photo,
--          illustration or other
--   title  the title printed in the image ('' if none);  text: all the text in reading order
--   raw_output is the model's reply as received; the tokens and cost are those of the call
CREATE TABLE IF NOT EXISTS gb_image_extractions (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	image_artifact_id INTEGER NOT NULL REFERENCES gb_image_artifacts(id) ON DELETE CASCADE,
	provider          TEXT NOT NULL,
	model             TEXT NOT NULL,
	theme             TEXT NOT NULL,
	title             TEXT NOT NULL DEFAULT '',
	text              TEXT NOT NULL DEFAULT '',
	raw_output        TEXT NOT NULL DEFAULT '',
	input_tokens      INTEGER NOT NULL DEFAULT 0,
	output_tokens     INTEGER NOT NULL DEFAULT 0,
	cost_usd          REAL NOT NULL DEFAULT 0,
	created_at        DATETIME NOT NULL
);

-- The text regions of an extraction, in reading order (position, from 0). kind is the role of the text: title,
-- subtitle, heading, paragraph, caption, label, legend, axis, annotation, table or other.
CREATE TABLE IF NOT EXISTS gb_image_text_blocks (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	extraction_id INTEGER NOT NULL REFERENCES gb_image_extractions(id) ON DELETE CASCADE,
	position      INTEGER NOT NULL,
	kind          TEXT NOT NULL,
	text          TEXT NOT NULL,
	UNIQUE (extraction_id, position)
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

// initSchema applies the graph schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init graph schema: %w", err)
	}
	return nil
}
