package sqlite

import (
	"database/sql"
	"fmt"
)

//---------------------------------------------------------------------------------------------

const schema = `
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

-- The AI models gligabao knows (see ai.SupportedModels). The graph tables reference them: gb_entities.model_id and
-- gb_edges.model_id say which model inferred the row.
--   id         derived from the name alone (model.ModelID: first 8 bytes of the SHA-256 of the lowercased name, hex), so a
--              model has the same ID in every database
--   name       the model name as the provider's API reports it (or the price-table key of a supported family)
--   supported  1 for the models gligabao supports; they are (re)inserted on every startup, which clears the flag of the
--              others. 0 for a model only seen in an API response, which is inserted when something it inferred is stored
CREATE TABLE IF NOT EXISTS gb_models (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	provider   TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL DEFAULT 'llm' CHECK (kind IN ('llm', 'embedding')),
	supported  INTEGER NOT NULL DEFAULT 0 CHECK (supported IN (0, 1)),
	created_at DATETIME NOT NULL DEFAULT (` + SQLNow + `)
);

-- One row per invocation of a model: the dynamic counterpart of gb_models, which statically lists the models. Every LLM
-- call, embedding request, web search and Jev classification the system makes is one row, written as the call happens
-- (so what was spent is kept even if the operation fails later).
--   type       the operation that made the call: 'remember', 'remember_image' (the image pipeline) or 'query'
--   stage      the pipeline stage: 'extract_graph', 'summarize_chunk', 'resolve_edges', 'resolve_series', 'extract_image',
--              'web_search', 'classify_complexity', 'embed' (remember) or 'query'; '' if unknown
--   model_id   the model that was run, as the API reported it (a reference to gb_models); NULL if it did not say
--   reasoning  the thinking level it ran with: 'none', 'low', 'medium' or 'high'. It is the level that was applied, so
--              'none' for a model without thinking (embeddings, Jev, web search, models that do not support it). A chunk
--              summary artifact references its run (gb_artifacts.inference_id) for the model and the level.
--   input_tokens, output_tokens  the usage of the call (the thinking tokens are in output_tokens)
--   cost_usd   total cost of the call in USD (0 if the model's price is unknown)
CREATE TABLE IF NOT EXISTS gb_inferences (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	type          TEXT NOT NULL,
	stage         TEXT NOT NULL DEFAULT '',
	model_id      TEXT REFERENCES gb_models(id),
	reasoning     TEXT NOT NULL DEFAULT 'none' CHECK (reasoning IN ('none', 'low', 'medium', 'high')),
	input_tokens  INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cost_usd      REAL NOT NULL,
	created_at    DATETIME NOT NULL DEFAULT (` + SQLNow + `)
);
CREATE INDEX IF NOT EXISTS idx_gb_inferences_type ON gb_inferences(type);
CREATE INDEX IF NOT EXISTS idx_gb_inferences_model ON gb_inferences(model_id);

-- Remember runs: ONE ROW PER EXECUTION OF THE REMEMBER PIPELINE, the authoritative record of how it went, stage by stage
-- (see internal/memory/memory_remember/runlog.go; the table is created by rememberRunsSchema in db_remember_run.go).
--   user_id, dataset (the name), kind (the artifact kind)  what was remembered
--   params       JSON object: how the call was made (reference time, session, models and reasoning per stage, chunk size,
--                which optional stages were asked for)
--   artifact_id  the artifact the text was stored as (the parent, for a chunked text); NULL until it is stored
--   status       of the call as a whole, so that it can be checked without looking at the stages: 'started', then
--                'succeeded' if the call returned a result or 'failed' if it returned an error ('not_started' exists for
--                the stages). A row that stays 'started' belongs to a process that died. The stages that never fail the
--                call (resolve_geo, web_search, fetch_series, observe_series, classify_complexity) can be 'failed' in a 'succeeded' run.
--   error        why the call failed; NULL if it did not
--   started_at, finished_at  of the call
-- and, for each stage of the pipeline (model.RememberStages: classify_complexity, chunk_text, extract_graph,
-- summarize_chunk, load_candidates, resolve_edges, store, store_series, resolve_geo, web_search, fetch_series, observe_series):
--   <stage>_status       'not_started' (not reached, or not used by the call), 'started', 'succeeded' or 'failed'
--   <stage>_error        why the stage failed; NULL if it did not
--   <stage>_started_at, <stage>_finished_at   when the stage began and when it last ended (a stage that runs for each chunk
--                        of a text spans all the chunks)
--   <stage>_inference_ids  only for the stages that run a model: JSON array of the IDs (gb_inferences) of the runs the stage
--                        made, in order. A list, because a stage makes one run per chunk (and an embedding request is a run).
-- All the DATETIME columns follow the convention of db_time.go.
`

// initSchema applies the database schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema + rememberRunsSchema()); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
