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

-- EVALS: one set of tables for both kinds of eval ('knowledge' and 'training'), the universal ones with a type column and
-- a metadata table for each kind (see sqlite/db_schema.go for what the columns mean). The composite foreign keys
-- (<parent id>, type) -> parent (id, type) keep the type the same down the chain.
CREATE TABLE IF NOT EXISTS gb_experiments (
	id          BIGSERIAL PRIMARY KEY,
	type        TEXT NOT NULL CHECK (type IN ('knowledge', 'training')),
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	config      JSONB NOT NULL DEFAULT '{}',
	created_at  TIMESTAMPTZ NOT NULL,
	UNIQUE (type, name),
	UNIQUE (id, type)
);

CREATE TABLE IF NOT EXISTS gb_experiment_cases (
	experiment_id BIGINT NOT NULL,
	type          TEXT NOT NULL,
	eval         TEXT NOT NULL DEFAULT '',
	case_name     TEXT NOT NULL,
	created_at    TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (experiment_id, eval, case_name),
	FOREIGN KEY (experiment_id, type) REFERENCES gb_experiments(id, type) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS gb_experiment_runs (
	id            BIGSERIAL PRIMARY KEY,
	type          TEXT NOT NULL CHECK (type IN ('knowledge', 'training')),
	experiment_id BIGINT,
	status        TEXT NOT NULL CHECK (status IN ('running', 'passed', 'failed', 'completed', 'cancelled')),
	params        JSONB NOT NULL DEFAULT '{}',
	total_cases   BIGINT NOT NULL DEFAULT 0,
	passed_cases  BIGINT NOT NULL DEFAULT 0,
	failed_cases  BIGINT NOT NULL DEFAULT 0,
	cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
	started_at    TIMESTAMPTZ NOT NULL,
	finished_at   TIMESTAMPTZ,
	UNIQUE (id, type),
	CHECK (type <> 'knowledge' OR experiment_id IS NOT NULL),
	FOREIGN KEY (experiment_id, type) REFERENCES gb_experiments(id, type) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_runs_experiment ON gb_experiment_runs(experiment_id);

CREATE TABLE IF NOT EXISTS gb_experiment_case_results (
	id          BIGSERIAL PRIMARY KEY,
	type        TEXT NOT NULL CHECK (type IN ('knowledge', 'training')),
	run_id      BIGINT NOT NULL,
	eval       TEXT NOT NULL DEFAULT '',
	case_name   TEXT NOT NULL,
	status      TEXT NOT NULL CHECK (status IN ('running', 'passed', 'failed', 'error', 'completed', 'cancelled')),
	error       TEXT NOT NULL DEFAULT '',
	cost_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
	started_at  TIMESTAMPTZ NOT NULL,
	finished_at TIMESTAMPTZ,
	UNIQUE (id, type),
	FOREIGN KEY (run_id, type) REFERENCES gb_experiment_runs(id, type) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_case_results_run ON gb_experiment_case_results(run_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_experiment_case_results_training_case ON gb_experiment_case_results(run_id, eval, case_name) WHERE type = 'training';

CREATE TABLE IF NOT EXISTS gb_experiment_trajectories (
	id            BIGSERIAL PRIMARY KEY,
	case_result_id   BIGINT NOT NULL REFERENCES gb_experiment_case_results(id) ON DELETE CASCADE,
	seq           BIGINT NOT NULL,
	kind          TEXT NOT NULL CHECK (kind IN ('remember', 'recall')),
	trajectory_id TEXT NOT NULL,
	agent_name    TEXT NOT NULL DEFAULT '',
	steps         BIGINT NOT NULL DEFAULT 0,
	cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
	path          TEXT NOT NULL DEFAULT '',
	artifact_ids  JSONB NOT NULL DEFAULT '[]',
	created_at    TIMESTAMPTZ NOT NULL,
	UNIQUE (case_result_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_trajectories_tid ON gb_experiment_trajectories(trajectory_id);

CREATE TABLE IF NOT EXISTS gb_knowledge_case_meta (
	case_result_id   BIGINT PRIMARY KEY,
	type          TEXT NOT NULL DEFAULT 'knowledge' CHECK (type = 'knowledge'),
	answer        TEXT NOT NULL DEFAULT '',
	problems      JSONB NOT NULL DEFAULT '[]',
	passed_checks JSONB NOT NULL DEFAULT '[]',
	dataset_id    BIGINT REFERENCES gb_datasets(id),
	steps         BIGINT NOT NULL DEFAULT 0,
	FOREIGN KEY (case_result_id, type) REFERENCES gb_experiment_case_results(id, type) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS gb_training_eval_run_meta (
	run_id              BIGINT PRIMARY KEY,
	type                TEXT NOT NULL DEFAULT 'training' CHECK (type = 'training'),
	trained_model_id    BIGINT NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	scheme              TEXT NOT NULL DEFAULT 'finetune_dataset' CHECK (scheme IN ('finetune_dataset')),
	dataset             TEXT NOT NULL DEFAULT '',
	dataset_sha256      TEXT NOT NULL DEFAULT '',
	judge_model         TEXT NOT NULL DEFAULT '',
	num_examples        BIGINT NOT NULL DEFAULT 0,
	num_answered        BIGINT NOT NULL DEFAULT 0,
	num_judged          BIGINT NOT NULL DEFAULT 0,
	num_failed          BIGINT NOT NULL DEFAULT 0,
	num_correct         BIGINT NOT NULL DEFAULT 0,
	num_partial         BIGINT NOT NULL DEFAULT 0,
	num_incorrect       BIGINT NOT NULL DEFAULT 0,
	score_sum           BIGINT NOT NULL DEFAULT 0,
	num_batches         BIGINT NOT NULL DEFAULT 0,
	judge_calls         BIGINT NOT NULL DEFAULT 0,
	judge_input_tokens  BIGINT NOT NULL DEFAULT 0,
	judge_output_tokens BIGINT NOT NULL DEFAULT 0,
	judge_cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
	error               TEXT NOT NULL DEFAULT '',
	FOREIGN KEY (run_id, type) REFERENCES gb_experiment_runs(id, type) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_training_eval_run_meta_model ON gb_training_eval_run_meta(trained_model_id, run_id);

-- Deleting a trained model deletes the metadata of its eval runs; this trigger deletes their runs too (see sqlite/db_schema.go).
CREATE OR REPLACE FUNCTION gb_training_eval_run_meta_deleted() RETURNS trigger AS $$
BEGIN
	DELETE FROM gb_experiment_runs WHERE type = 'training' AND id = OLD.run_id;
	RETURN NULL;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_gb_training_eval_run_meta_deleted ON gb_training_eval_run_meta;
CREATE TRIGGER trg_gb_training_eval_run_meta_deleted AFTER DELETE ON gb_training_eval_run_meta
	FOR EACH ROW EXECUTE FUNCTION gb_training_eval_run_meta_deleted();

CREATE TABLE IF NOT EXISTS gb_training_case_meta (
	case_result_id BIGINT PRIMARY KEY,
	type          TEXT NOT NULL DEFAULT 'training' CHECK (type = 'training'),
	idx           BIGINT NOT NULL,
	batch         BIGINT NOT NULL,
	instruction   TEXT NOT NULL,
	input         TEXT NOT NULL DEFAULT '',
	reference     TEXT NOT NULL DEFAULT '',
	response      TEXT NOT NULL DEFAULT '',
	score         BIGINT,
	reason        TEXT NOT NULL DEFAULT '',
	input_tokens  BIGINT NOT NULL DEFAULT 0,
	output_tokens BIGINT NOT NULL DEFAULT 0,
	duration_ms   BIGINT NOT NULL DEFAULT 0,
	FOREIGN KEY (case_result_id, type) REFERENCES gb_experiment_case_results(id, type) ON DELETE CASCADE
);`

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
