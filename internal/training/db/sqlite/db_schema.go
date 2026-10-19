package sqlite

import (
	"database/sql"
	"fmt"
	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------

// The training tables are created in the same database as the relational data, which is opened by
// internal/db/relational/sqlite (its timestamp convention applies: all DATETIME columns hold RFC 3339 UTC strings).
const schema = `
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
	created_at      DATETIME NOT NULL DEFAULT (` + rsqlite.SQLNow + `),
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
	created_at DATETIME NOT NULL DEFAULT (` + rsqlite.SQLNow + `),
	PRIMARY KEY (run_id, seq)
);

-- Trained models: ONE ROW PER COMPLETED TRAINING JOB, the set of weights the job saved on the sidecar's models volume (a LoRA
-- adapter: a few files). Training again never replaces a model, it makes a new row; any row can be picked to run inference on.
-- The row is made from the job's "completed" event (internal/training/monitor.go).
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
	created_at      DATETIME NOT NULL DEFAULT (` + rsqlite.SQLNow + `)
);
CREATE INDEX IF NOT EXISTS idx_gb_trained_models_run ON gb_trained_models(run_id);

-- Inference requests to trained models: ONE ROW PER REQUEST to run a prompt through a trained model (internal/training/inference.go).
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
	created_at       DATETIME NOT NULL DEFAULT (` + rsqlite.SQLNow + `),
	completed_at     DATETIME
);
CREATE INDEX IF NOT EXISTS idx_gb_trained_model_inferences_model ON gb_trained_model_inferences(trained_model_id, id);

-- JSONL training datasets (internal/training): the datasets are subdirectories of the training datasets directory
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
	created_at  DATETIME NOT NULL DEFAULT (` + rsqlite.SQLNow + `),
	updated_at  DATETIME NOT NULL DEFAULT (` + rsqlite.SQLNow + `),
	synced_at   DATETIME
);`

// initSchema applies the training schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init training schema: %w", err)
	}
	return nil
}
