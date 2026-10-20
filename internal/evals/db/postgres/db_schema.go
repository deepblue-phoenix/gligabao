package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

//---------------------------------------------------------------------------------------------

// The same tables as the sqlite backend (see sqlite/db_schema.go for what the columns mean), with native types. They reference
// gb_trained_models and gb_datasets, which are created by the relational backend (internal/db/relational/postgres), so that
// schema must be applied first.
const schema = `
-- EVALS: one set of tables for both kinds of eval ('knowledge' and 'training'), the universal ones with a type column and
-- a metadata table for each kind (see sqlite/db_schema.go for what the columns mean). The composite foreign keys
-- (<parent id>, type) -> parent (id, type) keep the type the same down the chain.
CREATE TABLE IF NOT EXISTS gb_experiments_defs (
	id          BIGSERIAL PRIMARY KEY,
	type        TEXT NOT NULL CHECK (type IN ('knowledge', 'training')),
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	config      JSONB NOT NULL DEFAULT '{}',
	created_at  TIMESTAMPTZ NOT NULL,
	UNIQUE (type, name),
	UNIQUE (id, type)
);

CREATE TABLE IF NOT EXISTS gb_experiment_cases_defs (
	experiment_id BIGINT NOT NULL,
	type          TEXT NOT NULL,
	eval         TEXT NOT NULL DEFAULT '',
	case_name     TEXT NOT NULL,
	created_at    TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (experiment_id, eval, case_name),
	FOREIGN KEY (experiment_id, type) REFERENCES gb_experiments_defs(id, type) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS gb_experiment_runs (
	id            BIGSERIAL PRIMARY KEY,
	type          TEXT NOT NULL CHECK (type IN ('knowledge', 'training')),
	experiment_id BIGINT,
	status        TEXT NOT NULL,
	params        JSONB NOT NULL DEFAULT '{}',
	total_cases   BIGINT NOT NULL DEFAULT 0,
	passed_cases  BIGINT NOT NULL DEFAULT 0,
	failed_cases  BIGINT NOT NULL DEFAULT 0,
	error_cases   BIGINT NOT NULL DEFAULT 0,
	judged_cases  BIGINT NOT NULL DEFAULT 0,
	cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
	started_at    TIMESTAMPTZ NOT NULL,
	finished_at   TIMESTAMPTZ,
	UNIQUE (id, type),
	CHECK (type <> 'knowledge' OR experiment_id IS NOT NULL),
	CHECK (status = 'running'
		OR (type = 'knowledge' AND status IN ('passed', 'failed'))
		OR (type = 'training' AND status IN ('completed', 'error', 'cancelled'))),
	FOREIGN KEY (experiment_id, type) REFERENCES gb_experiments_defs(id, type) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_runs_experiment ON gb_experiment_runs(experiment_id);

CREATE TABLE IF NOT EXISTS gb_experiment_case_runs (
	id          BIGSERIAL PRIMARY KEY,
	type        TEXT NOT NULL CHECK (type IN ('knowledge', 'training')),
	run_id      BIGINT NOT NULL,
	eval       TEXT NOT NULL DEFAULT '',
	case_name   TEXT NOT NULL,
	status      TEXT NOT NULL,
	error       TEXT NOT NULL DEFAULT '',
	cost_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
	started_at  TIMESTAMPTZ NOT NULL,
	finished_at TIMESTAMPTZ,
	UNIQUE (id, type),
	CHECK ((type = 'knowledge' AND status IN ('passed', 'failed', 'error')) OR (type = 'training' AND status IN ('judged', 'error'))),
	FOREIGN KEY (run_id, type) REFERENCES gb_experiment_runs(id, type) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_case_runs_run ON gb_experiment_case_runs(run_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_experiment_case_runs_training_case ON gb_experiment_case_runs(run_id, eval, case_name) WHERE type = 'training';

CREATE TABLE IF NOT EXISTS gb_experiment_case_runs_trajectories (
	id            BIGSERIAL PRIMARY KEY,
	case_run_id   BIGINT NOT NULL REFERENCES gb_experiment_case_runs(id) ON DELETE CASCADE,
	seq           BIGINT NOT NULL,
	kind          TEXT NOT NULL CHECK (kind IN ('remember', 'recall')),
	trajectory_id TEXT NOT NULL,
	agent_name    TEXT NOT NULL DEFAULT '',
	steps         BIGINT NOT NULL DEFAULT 0,
	cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
	path          TEXT NOT NULL DEFAULT '',
	artifact_ids  JSONB NOT NULL DEFAULT '[]',
	created_at    TIMESTAMPTZ NOT NULL,
	UNIQUE (case_run_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_gb_experiment_case_runs_trajectories_tid ON gb_experiment_case_runs_trajectories(trajectory_id);

CREATE TABLE IF NOT EXISTS gb_knowledge_case_meta (
	case_run_id   BIGINT PRIMARY KEY,
	type          TEXT NOT NULL DEFAULT 'knowledge' CHECK (type = 'knowledge'),
	answer        TEXT NOT NULL DEFAULT '',
	problems      JSONB NOT NULL DEFAULT '[]',
	passed_checks JSONB NOT NULL DEFAULT '[]',
	dataset_id    BIGINT REFERENCES gb_datasets(id),
	steps         BIGINT NOT NULL DEFAULT 0,
	FOREIGN KEY (case_run_id, type) REFERENCES gb_experiment_case_runs(id, type) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS gb_training_eval_run_meta (
	run_id              BIGINT PRIMARY KEY,
	type                TEXT NOT NULL DEFAULT 'training' CHECK (type = 'training'),
	trained_model_id    BIGINT NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	eval                TEXT NOT NULL,
	judge_model         TEXT NOT NULL DEFAULT '',
	num_examples        BIGINT NOT NULL DEFAULT 0,
	num_answered        BIGINT NOT NULL DEFAULT 0,
	num_judged          BIGINT NOT NULL DEFAULT 0,
	num_errored         BIGINT NOT NULL DEFAULT 0,
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
	case_run_id BIGINT PRIMARY KEY,
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
	FOREIGN KEY (case_run_id, type) REFERENCES gb_experiment_case_runs(id, type) ON DELETE CASCADE
);

-- TRAINING DATA AUDITS of trained models: one table of runs and one of cases, separate from the evals above (see sqlite/db_schema.go).
CREATE TABLE IF NOT EXISTS gb_training_audit_runs (
	id                  BIGSERIAL PRIMARY KEY,
	trained_model_id    BIGINT NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	status              TEXT NOT NULL CHECK (status IN ('running', 'completed', 'error', 'cancelled')),
	dataset             TEXT NOT NULL DEFAULT '',
	dataset_sha256      TEXT NOT NULL DEFAULT '',
	params              JSONB NOT NULL DEFAULT '{}',
	judge_model         TEXT NOT NULL DEFAULT '',
	num_examples        BIGINT NOT NULL DEFAULT 0,
	num_answered        BIGINT NOT NULL DEFAULT 0,
	num_judged          BIGINT NOT NULL DEFAULT 0,
	num_errored         BIGINT NOT NULL DEFAULT 0,
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
	started_at          TIMESTAMPTZ NOT NULL,
	finished_at         TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_gb_training_audit_runs_model ON gb_training_audit_runs(trained_model_id, id);

CREATE TABLE IF NOT EXISTS gb_training_audit_cases (
	id            BIGSERIAL PRIMARY KEY,
	run_id        BIGINT NOT NULL REFERENCES gb_training_audit_runs(id) ON DELETE CASCADE,
	idx           BIGINT NOT NULL,
	batch         BIGINT NOT NULL,
	instruction   TEXT NOT NULL,
	input         TEXT NOT NULL DEFAULT '',
	reference     TEXT NOT NULL DEFAULT '',
	response      TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL CHECK (status IN ('judged', 'error')),
	error         TEXT NOT NULL DEFAULT '',
	score         BIGINT,
	reason        TEXT NOT NULL DEFAULT '',
	input_tokens  BIGINT NOT NULL DEFAULT 0,
	output_tokens BIGINT NOT NULL DEFAULT 0,
	duration_ms   BIGINT NOT NULL DEFAULT 0,
	UNIQUE (run_id, idx)
);`

// initSchema applies the eval schema. Concurrent starters are serialized with an advisory lock, because
// CREATE ... IF NOT EXISTS can still race in PostgreSQL.
func initSchema(ctx context.Context, d *sql.DB) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("init evals schema: %w", err)
	}
	defer conn.Close()
	const lockID = 0x67620003 // arbitrary application-wide key (relational uses 0x67620001, graph 0x67620002)
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(lockID)); err != nil {
		return fmt.Errorf("init evals schema: lock: %w", err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(lockID))
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("init evals schema: %w", err)
	}
	return nil
}
