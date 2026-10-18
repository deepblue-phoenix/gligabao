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
--   type       the operation that made the call: 'remember', 'remember_image' (the image pipeline) or 'recall'
--   stage      the pipeline stage: 'extract_graph', 'summarize_chunk', 'resolve_edges', 'resolve_series', 'extract_image',
--              'web_search', 'classify_complexity', 'embed' (remember) or 'recall'; '' if unknown
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
--                call (web_search, fetch_series, observe_series, classify_complexity) can be 'failed' in a 'succeeded' run.
--   error        why the call failed; NULL if it did not
--   started_at, finished_at  of the call
-- and, for each stage of the pipeline (model.RememberStages: classify_complexity, chunk_text, extract_graph,
-- summarize_chunk, load_candidates, resolve_edges, store, store_series, web_search, fetch_series, observe_series):
--   <stage>_status       'not_started' (not reached, or not used by the call), 'started', 'succeeded' or 'failed'
--   <stage>_error        why the stage failed; NULL if it did not
--   <stage>_started_at, <stage>_finished_at   when the stage began and when it last ended (a stage that runs for each chunk
--                        of a text spans all the chunks)
--   <stage>_inference_ids  only for the stages that run a model: JSON array of the IDs (gb_inferences) of the runs the stage
--                        made, in order. A list, because a stage makes one run per chunk (and an embedding request is a run).
-- All the DATETIME columns follow the convention of db_time.go.

-- Training jobs started on the sidecar (internal/sidecar, sidecar/ai_train): ONE ROW PER JOB the sidecar reported as started,
-- made from the TrainingJob it returned from POST /training/job_start.
--   job_id           the sidecar's ID of the job (names its progress file there); provider_job_id is the compute provider's
--   status           'started' when the sidecar reports the job started, then the job's status events ('running', 'training'),
--                    finally 'completed' or 'failed'
--   model_size       '4b' or '12b'
--   dataset          the file on the sidecar's data volume the job trains on ('' only in a job started before a dataset was required)
--   num_train_steps  how many steps the job was asked to train
--   last_step, last_loss  from the newest progress event that has a loss; final_loss: the mean loss, when the job completed
--   error            why the job failed
--   started_at       when the sidecar says the job started; created_at is when the row was made; finished_at when it ended
CREATE TABLE IF NOT EXISTS gb_training_job_runs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id          TEXT NOT NULL,
	provider_job_id TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL,
	model_size      TEXT NOT NULL,
	dataset         TEXT NOT NULL DEFAULT '',
	num_train_steps INTEGER NOT NULL,
	last_step       INTEGER NOT NULL DEFAULT 0,
	last_loss       REAL,
	final_loss      REAL,
	error           TEXT NOT NULL DEFAULT '',
	started_at      DATETIME NOT NULL,
	created_at      DATETIME NOT NULL DEFAULT (` + SQLNow + `),
	finished_at     DATETIME
);
CREATE INDEX IF NOT EXISTS idx_gb_training_job_runs_job ON gb_training_job_runs(job_id);

-- The events the sidecar reported for a training job (status changes and progress with the loss), in the order of the
-- sidecar's progress file: seq is the event's index there, so a reconnect can resume after the last seq stored.
--   data: the event as JSON (internal/sidecar: TrainingEvent)
CREATE TABLE IF NOT EXISTS gb_training_job_events (
	run_id     INTEGER NOT NULL REFERENCES gb_training_job_runs(id) ON DELETE CASCADE,
	seq        INTEGER NOT NULL,
	type       TEXT NOT NULL,
	data       TEXT NOT NULL,
	created_at DATETIME NOT NULL DEFAULT (` + SQLNow + `),
	PRIMARY KEY (run_id, seq)
);

-- Trained models: ONE ROW PER COMPLETED TRAINING JOB, the set of weights the job saved on the sidecar's models volume (a LoRA
-- adapter: a few files). Training again never replaces a model, it makes a new row; any row can be picked to run inference on.
-- The row is made from the job's "completed" event (internal/api/training/monitor.go).
--   run_id           the job run (gb_training_job_runs) that made it; NULL if that run was deleted. job_id is unique: a job makes one model
--   name             for lists; made from the settings when the row is made
--   base_model       the model that was fine-tuned (e.g. google/gemma-4-E4B-it); model_size '4b' or '12b' (inference needs it)
--   dataset          the dataset file name ('' = the sidecar's default); dataset_sha256 the digest of the file the job read; num_examples its examples
--   num_train_steps, final_loss  how long the job trained and its mean training loss
--   params           JSON object: the other training settings (LoRA rank and alpha, learning rate, batch size, ...)
--   path             the directory of the weights on the models volume, relative to it ('trained/<job id>'): inference's model_path
--   files            JSON array of {path, size_bytes, sha256}: the files in that directory; size_bytes their total
CREATE TABLE IF NOT EXISTS gb_trained_models (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	name            TEXT NOT NULL,
	run_id          INTEGER REFERENCES gb_training_job_runs(id) ON DELETE SET NULL,
	job_id          TEXT NOT NULL UNIQUE,
	base_model      TEXT NOT NULL DEFAULT '',
	model_size      TEXT NOT NULL,
	dataset         TEXT NOT NULL DEFAULT '',
	dataset_sha256  TEXT NOT NULL DEFAULT '',
	num_examples    INTEGER NOT NULL DEFAULT 0,
	num_train_steps INTEGER NOT NULL,
	final_loss      REAL,
	params          TEXT NOT NULL DEFAULT '{}',
	path            TEXT NOT NULL,
	files           TEXT NOT NULL DEFAULT '[]',
	size_bytes      INTEGER NOT NULL DEFAULT 0,
	created_at      DATETIME NOT NULL DEFAULT (` + SQLNow + `)
);
CREATE INDEX IF NOT EXISTS idx_gb_trained_models_run ON gb_trained_models(run_id);

-- Inference requests to trained models: ONE ROW PER REQUEST to run a prompt through a trained model (internal/api/training/models.go).
-- The row is made when the request starts (status 'running') and updated when the sidecar answers or fails, so every request can be
-- reviewed afterwards, failed ones too. A row still 'running' long after its start was interrupted (e.g. the server restarted).
--   trained_model_id  the model (gb_trained_models) the prompt was run through
--   prompt, response  what was sent and the model's reply ('' until completed)
--   status            'running', 'completed' or 'failed'; error is the reason of a failure
--   model_size, model_path  what the sidecar was asked to load (copied from the trained model)
--   max_tokens        0 = the sidecar's default; temperature NULL = the sidecar's default
--   input_tokens, output_tokens  token counts reported by the model (the prompt as tokenized, and what it generated); 0 if unknown
--   cold_start        1 if the request had to load the model into a GPU container, 0 if it found it loaded, NULL if unknown
--   load_seconds      how long that load took (0 when not a cold start or unknown)
--   duration_ms       how long the request took; completed_at when it ended (NULL while running)
CREATE TABLE IF NOT EXISTS gb_trained_model_inferences (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	trained_model_id INTEGER NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	prompt           TEXT NOT NULL,
	response         TEXT NOT NULL DEFAULT '',
	status           TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
	error            TEXT NOT NULL DEFAULT '',
	model_size       TEXT NOT NULL DEFAULT '',
	model_path       TEXT NOT NULL DEFAULT '',
	max_tokens       INTEGER NOT NULL DEFAULT 0,
	temperature      REAL,
	input_tokens     INTEGER NOT NULL DEFAULT 0,
	output_tokens    INTEGER NOT NULL DEFAULT 0,
	cold_start       INTEGER,
	load_seconds     REAL NOT NULL DEFAULT 0,
	duration_ms      INTEGER NOT NULL DEFAULT 0,
	created_at       DATETIME NOT NULL DEFAULT (` + SQLNow + `),
	completed_at     DATETIME
);
CREATE INDEX IF NOT EXISTS idx_gb_trained_model_inferences_model ON gb_trained_model_inferences(trained_model_id, id);

-- LLM-as-a-judge evals of trained models (evals/training, internal/api/training/evals.go): ONE ROW PER RUN. The trained model is asked
-- the questions of a dataset (its training dataset for now) and an external LLM scores its answers against the dataset's, a batch of
-- questions per LLM call. The counters are rewritten after every batch, so the row shows the progress while the eval runs. A row still
-- 'running' after a restart of the server is set to 'failed' when the server starts.
--   status           'running', 'completed', 'failed' (error says why) or 'cancelled'
--   dataset, dataset_sha256  the dataset the questions came from, and the digest of the file that was read
--   params           JSON object: batch_size, concurrency, max_tokens, temperature, limit
--   judge_model      the model that judged ('' until its first reply)
--   num_examples     the questions asked; num_answered the ones the model answered; num_judged the ones scored; num_failed the ones where
--                    the model or the judge failed; num_correct/num_partial/num_incorrect split num_judged by score (>= 4 / 2-3 / 0-1);
--                    score_sum the sum of the scores (0 to 5 each) of the judged ones
--   num_batches      judge batches done; judge_calls the LLM calls made (a batch may need a retry); judge_* its tokens and cost
CREATE TABLE IF NOT EXISTS gb_trained_model_evals (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	trained_model_id   INTEGER NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	status             TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed', 'cancelled')),
	dataset            TEXT NOT NULL DEFAULT '',
	dataset_sha256     TEXT NOT NULL DEFAULT '',
	params             TEXT NOT NULL DEFAULT '{}',
	judge_model        TEXT NOT NULL DEFAULT '',
	num_examples       INTEGER NOT NULL DEFAULT 0,
	num_answered       INTEGER NOT NULL DEFAULT 0,
	num_judged         INTEGER NOT NULL DEFAULT 0,
	num_failed         INTEGER NOT NULL DEFAULT 0,
	num_correct        INTEGER NOT NULL DEFAULT 0,
	num_partial        INTEGER NOT NULL DEFAULT 0,
	num_incorrect      INTEGER NOT NULL DEFAULT 0,
	score_sum          INTEGER NOT NULL DEFAULT 0,
	num_batches        INTEGER NOT NULL DEFAULT 0,
	judge_calls        INTEGER NOT NULL DEFAULT 0,
	judge_input_tokens INTEGER NOT NULL DEFAULT 0,
	judge_output_tokens INTEGER NOT NULL DEFAULT 0,
	judge_cost_usd     REAL NOT NULL DEFAULT 0,
	error              TEXT NOT NULL DEFAULT '',
	created_at         DATETIME NOT NULL DEFAULT (` + SQLNow + `),
	finished_at        DATETIME
);
CREATE INDEX IF NOT EXISTS idx_gb_trained_model_evals_model ON gb_trained_model_evals(trained_model_id, id);

-- The outcome for each question of an eval, written when its judge batch is done.
--   idx          the example's position in the dataset (0-based); batch the judge batch (1-based)
--   instruction, input  the question; reference the dataset's answer; response the model's ('' if it failed)
--   status       'judged' or 'failed' (error says why: the model or the judge)
--   score        the judge's, 0 to 5 (NULL unless judged); reason its explanation
--   input_tokens, output_tokens, duration_ms  of the model's answer
CREATE TABLE IF NOT EXISTS gb_trained_model_eval_items (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	eval_id       INTEGER NOT NULL REFERENCES gb_trained_model_evals(id) ON DELETE CASCADE,
	idx           INTEGER NOT NULL,
	batch         INTEGER NOT NULL,
	instruction   TEXT NOT NULL,
	input         TEXT NOT NULL DEFAULT '',
	reference     TEXT NOT NULL DEFAULT '',
	response      TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL CHECK (status IN ('judged', 'failed')),
	error         TEXT NOT NULL DEFAULT '',
	score         INTEGER,
	reason        TEXT NOT NULL DEFAULT '',
	input_tokens  INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	duration_ms   INTEGER NOT NULL DEFAULT 0,
	UNIQUE (eval_id, idx)
);

-- JSONL training datasets (internal/api/training): the datasets are subdirectories of the training datasets directory
-- that have a dataset.toml manifest; this table has ONE ROW PER DATASET, made when the Go server first sees the data file
-- and replaced when it sees it changed. The sidecar is not involved, it gets the file when a training job that uses it
-- is started.
--   filename     the dataset's name plus ".jsonl" (the file name it has on the sidecar), unique
--   size_bytes, num_records (non-empty lines), sha256 (hex) of the file
--   status       'stored' (only here) or 'synced' (the sidecar has this version of the file)
--   updated_at   when the row last described a new version of the file; synced_at when the sidecar got it (NULL while it has not)
CREATE TABLE IF NOT EXISTS gb_training_datasets (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	filename    TEXT NOT NULL UNIQUE,
	size_bytes  INTEGER NOT NULL,
	num_records INTEGER NOT NULL,
	sha256      TEXT NOT NULL,
	status      TEXT NOT NULL CHECK (status IN ('stored', 'synced')),
	created_at  DATETIME NOT NULL DEFAULT (` + SQLNow + `),
	updated_at  DATETIME NOT NULL DEFAULT (` + SQLNow + `),
	synced_at   DATETIME
);

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
--   dataset_id: the dataset the eval ran in (NULL if it was never created); it holds the graph the eval built
CREATE TABLE IF NOT EXISTS gb_experiment_run_evals (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id      INTEGER NOT NULL REFERENCES gb_experiment_runs(id) ON DELETE CASCADE,
	suite       TEXT NOT NULL,
	eval_name   TEXT NOT NULL,
	status      TEXT NOT NULL CHECK (status IN ('passed', 'failed', 'error')),
	answer      TEXT NOT NULL DEFAULT '',
	problems    TEXT NOT NULL DEFAULT '[]',
	passed_checks TEXT NOT NULL DEFAULT '[]',
	dataset_id  INTEGER REFERENCES gb_datasets(id),
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
--   artifact_ids: JSON array of the IDs of the artifacts (in the run eval's dataset) the call is about (remember: its
--   input; recall: the evidence's sources)
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
	artifact_ids  TEXT NOT NULL DEFAULT '[]',
	created_at    DATETIME NOT NULL,
	UNIQUE (run_eval_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_trajectories_tid ON gb_experiment_trajectories(trajectory_id);`

// initSchema applies the database schema.
func initSchema(d *sql.DB) error {
	// A development build kept the dataset content in the table (in a `content` column); it is a file now. Drop that table.
	var legacy int
	if err := d.QueryRow(`SELECT count(*) FROM pragma_table_info('gb_training_datasets') WHERE name = 'content'`).Scan(&legacy); err == nil && legacy > 0 {
		if _, err := d.Exec(`DROP TABLE gb_training_datasets`); err != nil {
			return fmt.Errorf("init schema: drop legacy gb_training_datasets: %w", err)
		}
	}
	// Tables from before 0.46.0 lack the cold-start columns of gb_trained_model_inferences: add them.
	var tbl, hasCold int
	if err := d.QueryRow(`SELECT count(*) FROM pragma_table_info('gb_trained_model_inferences')`).Scan(&tbl); err == nil && tbl > 0 {
		if err := d.QueryRow(`SELECT count(*) FROM pragma_table_info('gb_trained_model_inferences') WHERE name = 'cold_start'`).Scan(&hasCold); err == nil && hasCold == 0 {
			if _, err := d.Exec(`ALTER TABLE gb_trained_model_inferences ADD COLUMN cold_start INTEGER;
				ALTER TABLE gb_trained_model_inferences ADD COLUMN load_seconds REAL NOT NULL DEFAULT 0`); err != nil {
				return fmt.Errorf("init schema: add cold_start columns: %w", err)
			}
		}
	}
	if _, err := d.Exec(schema + rememberRunsSchema()); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
