package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

//---------------------------------------------------------------------------------------------

// The same tables as the sqlite backend (see sqlite/db_schema.go for what the columns mean), with native types:
// BIGSERIAL ids, TIMESTAMPTZ times, BOOLEAN flags and JSONB tags.
const schema = `
CREATE TABLE IF NOT EXISTS gb_items (
	id         BIGSERIAL PRIMARY KEY,
	name       TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT ` + SQLNow + `
);

CREATE TABLE IF NOT EXISTS gb_datasets (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	tags        JSONB NOT NULL DEFAULT '[]',
	created_at  TIMESTAMPTZ NOT NULL,
	deleted     BOOLEAN NOT NULL DEFAULT FALSE,
	deleted_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_datasets_user_name
	ON gb_datasets(user_id, name)
	WHERE NOT deleted;

CREATE TABLE IF NOT EXISTS gb_ontologies (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	iri         TEXT NOT NULL DEFAULT '',
	title       TEXT NOT NULL DEFAULT '',
	format      TEXT NOT NULL,
	size_bytes  BIGINT NOT NULL,
	sha256      TEXT NOT NULL,
	classes     BIGINT NOT NULL,
	properties  BIGINT NOT NULL,
	edges       BIGINT NOT NULL,
	created_at  TIMESTAMPTZ NOT NULL,
	updated_at  TIMESTAMPTZ NOT NULL,
	UNIQUE (user_id, name)
);

-- The AI models gligabao knows (see sqlite/db_schema.go).
CREATE TABLE IF NOT EXISTS gb_models (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	provider   TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL DEFAULT 'llm' CHECK (kind IN ('llm', 'embedding')),
	supported  BOOLEAN NOT NULL DEFAULT FALSE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT ` + SQLNow + `
);

-- One row per invocation of a model (see sqlite/db_schema.go).
CREATE TABLE IF NOT EXISTS gb_inferences (
	id            BIGSERIAL PRIMARY KEY,
	type          TEXT NOT NULL,
	stage         TEXT NOT NULL DEFAULT '',
	model_id      TEXT REFERENCES gb_models(id),
	reasoning     TEXT NOT NULL DEFAULT 'none' CHECK (reasoning IN ('none', 'low', 'medium', 'high')),
	input_tokens  BIGINT NOT NULL,
	output_tokens BIGINT NOT NULL,
	cost_usd      DOUBLE PRECISION NOT NULL, -- total cost of the call in USD (0 if the model's price is unknown)
	created_at    TIMESTAMPTZ NOT NULL DEFAULT ` + SQLNow + `
);
CREATE INDEX IF NOT EXISTS idx_gb_inferences_type ON gb_inferences(type);
CREATE INDEX IF NOT EXISTS idx_gb_inferences_model ON gb_inferences(model_id);

-- One row per training job the sidecar started (see sqlite/db_schema.go).
CREATE TABLE IF NOT EXISTS gb_training_job_runs (
	id              BIGSERIAL PRIMARY KEY,
	job_id          TEXT NOT NULL,
	provider_job_id TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL,
	model_size      TEXT NOT NULL,
	dataset         TEXT NOT NULL DEFAULT '',
	num_train_steps BIGINT NOT NULL,
	last_step       BIGINT NOT NULL DEFAULT 0,
	last_loss       DOUBLE PRECISION,
	final_loss      DOUBLE PRECISION,
	error           TEXT NOT NULL DEFAULT '',
	started_at      TIMESTAMPTZ NOT NULL,
	created_at      TIMESTAMPTZ NOT NULL DEFAULT ` + SQLNow + `,
	finished_at     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_gb_training_job_runs_job ON gb_training_job_runs(job_id);

-- The events the sidecar reported for a training job (see sqlite/db_schema.go).
CREATE TABLE IF NOT EXISTS gb_training_job_events (
	run_id     BIGINT NOT NULL REFERENCES gb_training_job_runs(id) ON DELETE CASCADE,
	seq        BIGINT NOT NULL,
	type       TEXT NOT NULL,
	data       TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT ` + SQLNow + `,
	PRIMARY KEY (run_id, seq)
);

-- One row per trained model: the weights a completed training job saved (see sqlite/db_schema.go).
CREATE TABLE IF NOT EXISTS gb_trained_models (
	id              BIGSERIAL PRIMARY KEY,
	name            TEXT NOT NULL,
	run_id          BIGINT REFERENCES gb_training_job_runs(id) ON DELETE SET NULL,
	job_id          TEXT NOT NULL UNIQUE,
	base_model      TEXT NOT NULL DEFAULT '',
	model_size      TEXT NOT NULL,
	dataset         TEXT NOT NULL DEFAULT '',
	dataset_sha256  TEXT NOT NULL DEFAULT '',
	num_examples    BIGINT NOT NULL DEFAULT 0,
	num_train_steps BIGINT NOT NULL,
	final_loss      DOUBLE PRECISION,
	params          TEXT NOT NULL DEFAULT '{}',
	path            TEXT NOT NULL,
	files           TEXT NOT NULL DEFAULT '[]',
	size_bytes      BIGINT NOT NULL DEFAULT 0,
	created_at      TIMESTAMPTZ NOT NULL DEFAULT ` + SQLNow + `
);
CREATE INDEX IF NOT EXISTS idx_gb_trained_models_run ON gb_trained_models(run_id);

-- One row per inference request to a trained model (see sqlite/db_schema.go).
CREATE TABLE IF NOT EXISTS gb_trained_model_inferences (
	id               BIGSERIAL PRIMARY KEY,
	trained_model_id BIGINT NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	prompt           TEXT NOT NULL,
	response         TEXT NOT NULL DEFAULT '',
	status           TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
	error            TEXT NOT NULL DEFAULT '',
	model_size       TEXT NOT NULL DEFAULT '',
	model_path       TEXT NOT NULL DEFAULT '',
	max_tokens       BIGINT NOT NULL DEFAULT 0,
	temperature      DOUBLE PRECISION,
	input_tokens     BIGINT NOT NULL DEFAULT 0,
	output_tokens    BIGINT NOT NULL DEFAULT 0,
	cold_start       BOOLEAN,
	load_seconds     DOUBLE PRECISION NOT NULL DEFAULT 0,
	duration_ms      BIGINT NOT NULL DEFAULT 0,
	created_at       TIMESTAMPTZ NOT NULL DEFAULT ` + SQLNow + `,
	completed_at     TIMESTAMPTZ
);
ALTER TABLE gb_trained_model_inferences ADD COLUMN IF NOT EXISTS cold_start BOOLEAN;
ALTER TABLE gb_trained_model_inferences ADD COLUMN IF NOT EXISTS load_seconds DOUBLE PRECISION NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_gb_trained_model_inferences_model ON gb_trained_model_inferences(trained_model_id, id);

-- JSONL training datasets found in the training datasets directory by the Go server (see sqlite/db_schema.go).
CREATE TABLE IF NOT EXISTS gb_training_datasets (
	id          BIGSERIAL PRIMARY KEY,
	filename    TEXT NOT NULL UNIQUE,
	size_bytes  BIGINT NOT NULL,
	num_records BIGINT NOT NULL,
	sha256      TEXT NOT NULL,
	status      TEXT NOT NULL CHECK (status IN ('stored', 'synced')),
	created_at  TIMESTAMPTZ NOT NULL DEFAULT ` + SQLNow + `,
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT ` + SQLNow + `,
	synced_at   TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS gb_experiments (
	id          BIGSERIAL PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	config      JSONB NOT NULL DEFAULT '{}',
	created_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS gb_experiment_evals (
	experiment_id BIGINT NOT NULL REFERENCES gb_experiments(id) ON DELETE CASCADE,
	suite         TEXT NOT NULL,
	eval_name     TEXT NOT NULL,
	created_at    TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (experiment_id, suite, eval_name)
);

CREATE TABLE IF NOT EXISTS gb_experiment_runs (
	id            BIGSERIAL PRIMARY KEY,
	experiment_id BIGINT NOT NULL REFERENCES gb_experiments(id) ON DELETE CASCADE,
	status        TEXT NOT NULL CHECK (status IN ('running', 'passed', 'failed')),
	params        JSONB NOT NULL DEFAULT '{}',
	total_evals   BIGINT NOT NULL DEFAULT 0,
	passed_evals  BIGINT NOT NULL DEFAULT 0,
	failed_evals  BIGINT NOT NULL DEFAULT 0,
	cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
	started_at    TIMESTAMPTZ NOT NULL,
	finished_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_runs_experiment ON gb_experiment_runs(experiment_id);

CREATE TABLE IF NOT EXISTS gb_experiment_run_evals (
	id          BIGSERIAL PRIMARY KEY,
	run_id      BIGINT NOT NULL REFERENCES gb_experiment_runs(id) ON DELETE CASCADE,
	suite       TEXT NOT NULL,
	eval_name   TEXT NOT NULL,
	status      TEXT NOT NULL CHECK (status IN ('passed', 'failed', 'error')),
	answer      TEXT NOT NULL DEFAULT '',
	problems    JSONB NOT NULL DEFAULT '[]',
	passed_checks JSONB NOT NULL DEFAULT '[]',
	dataset_id  BIGINT REFERENCES gb_datasets(id),
	error       TEXT NOT NULL DEFAULT '',
	steps       BIGINT NOT NULL DEFAULT 0,
	cost_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
	started_at  TIMESTAMPTZ NOT NULL,
	finished_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_run_evals_run ON gb_experiment_run_evals(run_id);

CREATE TABLE IF NOT EXISTS gb_experiment_trajectories (
	id            BIGSERIAL PRIMARY KEY,
	run_eval_id   BIGINT NOT NULL REFERENCES gb_experiment_run_evals(id) ON DELETE CASCADE,
	seq           BIGINT NOT NULL,
	kind          TEXT NOT NULL CHECK (kind IN ('remember', 'recall')),
	trajectory_id TEXT NOT NULL,
	agent_name    TEXT NOT NULL DEFAULT '',
	steps         BIGINT NOT NULL DEFAULT 0,
	cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
	path          TEXT NOT NULL DEFAULT '',
	artifact_ids  JSONB NOT NULL DEFAULT '[]',
	created_at    TIMESTAMPTZ NOT NULL,
	UNIQUE (run_eval_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_trajectories_tid ON gb_experiment_trajectories(trajectory_id);`

// initSchema applies the database schema. Concurrent starters are serialized with an advisory lock, because
// CREATE ... IF NOT EXISTS can still race in PostgreSQL.
func initSchema(ctx context.Context, d *sql.DB) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	defer conn.Close()
	const lockID = 0x67620001 // arbitrary application-wide key
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(lockID)); err != nil {
		return fmt.Errorf("init schema: lock: %w", err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(lockID))
	// A development build kept the dataset content in the table (in a `content` column); it is a file now. Drop that table.
	if _, err := conn.ExecContext(ctx, `DO $$ BEGIN
		IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema()
			AND table_name = 'gb_training_datasets' AND column_name = 'content') THEN
			DROP TABLE gb_training_datasets;
		END IF; END $$`); err != nil {
		return fmt.Errorf("init schema: drop legacy gb_training_datasets: %w", err)
	}
	if _, err := conn.ExecContext(ctx, schema+rememberRunsSchema()); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
