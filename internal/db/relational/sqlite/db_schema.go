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
CREATE INDEX IF NOT EXISTS idx_gb_llm_calls_type ON gb_llm_calls(type);

-- Eval experiments (evals/ and internal/experiments). An experiment is a named series of eval runs; names are unique.
--   config: JSON object with the free-form settings under test
CREATE TABLE IF NOT EXISTS gb_experiments (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	config      TEXT NOT NULL DEFAULT '{}',
	created_at  DATETIME NOT NULL
);

-- The evals (a case name within a suite, i.e. a suite file name without ".json") that an experiment has executed.
-- A row is added the first time one of the experiment's runs executes the eval.
CREATE TABLE IF NOT EXISTS gb_experiment_evals (
	experiment_id INTEGER NOT NULL REFERENCES gb_experiments(id) ON DELETE CASCADE,
	suite         TEXT NOT NULL,
	eval_name     TEXT NOT NULL,
	created_at    DATETIME NOT NULL,
	PRIMARY KEY (experiment_id, suite, eval_name)
);

-- One execution of an experiment. status: 'running', then 'passed' or 'failed' once finished.
--   params: JSON object describing how the run was invoked
--   total_evals, passed_evals, failed_evals, cost_usd: totals over the run's evals, set when the run is finished
CREATE TABLE IF NOT EXISTS gb_experiment_runs (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	experiment_id INTEGER NOT NULL REFERENCES gb_experiments(id) ON DELETE CASCADE,
	status        TEXT NOT NULL CHECK (status IN ('running', 'passed', 'failed')),
	params        TEXT NOT NULL DEFAULT '{}',
	total_evals   INTEGER NOT NULL DEFAULT 0,
	passed_evals  INTEGER NOT NULL DEFAULT 0,
	failed_evals  INTEGER NOT NULL DEFAULT 0,
	cost_usd      REAL NOT NULL DEFAULT 0,
	started_at    DATETIME NOT NULL,
	finished_at   DATETIME
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_runs_experiment ON gb_experiment_runs(experiment_id);

-- The result of one eval within a run. status: 'passed', 'failed' (a check did not hold) or 'error' (the eval
-- could not be run to the end).
--   problems: JSON array of strings, the failed checks; passed_checks: JSON array of strings, the checks that held
--   graph: JSON snapshot of the knowledge graph the eval built ('' if none), the same shape the admin graph view shows
CREATE TABLE IF NOT EXISTS gb_experiment_run_evals (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id      INTEGER NOT NULL REFERENCES gb_experiment_runs(id) ON DELETE CASCADE,
	suite       TEXT NOT NULL,
	eval_name   TEXT NOT NULL,
	status      TEXT NOT NULL CHECK (status IN ('passed', 'failed', 'error')),
	answer      TEXT NOT NULL DEFAULT '',
	problems    TEXT NOT NULL DEFAULT '[]',
	passed_checks TEXT NOT NULL DEFAULT '[]',
	graph       TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT '',
	steps       INTEGER NOT NULL DEFAULT 0,
	cost_usd    REAL NOT NULL DEFAULT 0,
	started_at  DATETIME NOT NULL,
	finished_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_run_evals_run ON gb_experiment_run_evals(run_id);

-- The ATIF trajectories of an eval (internal/ai/atif), in call order. Only the reference is kept here:
-- trajectory_id is the ATIF trajectory_id, and path is the JSON file the trajectory was stored in ('' if none).
--   kind: 'remember' or 'recall'
--   artifacts: JSON array of the artifacts the call is about (remember: its input; recall: the evidence's sources)
CREATE TABLE IF NOT EXISTS gb_experiment_trajectories (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	run_eval_id   INTEGER NOT NULL REFERENCES gb_experiment_run_evals(id) ON DELETE CASCADE,
	seq           INTEGER NOT NULL,
	kind          TEXT NOT NULL CHECK (kind IN ('remember', 'recall')),
	trajectory_id TEXT NOT NULL,
	agent_name    TEXT NOT NULL DEFAULT '',
	steps         INTEGER NOT NULL DEFAULT 0,
	cost_usd      REAL NOT NULL DEFAULT 0,
	path          TEXT NOT NULL DEFAULT '',
	artifacts     TEXT NOT NULL DEFAULT '[]',
	created_at    DATETIME NOT NULL,
	UNIQUE (run_eval_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_trajectories_tid ON gb_experiment_trajectories(trajectory_id);`

// initSchema applies the database schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
