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
CREATE TABLE IF NOT EXISTS gb_model_runs (
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
CREATE INDEX IF NOT EXISTS idx_gb_model_runs_type ON gb_model_runs(type);
CREATE INDEX IF NOT EXISTS idx_gb_model_runs_model ON gb_model_runs(model_id);

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
	if _, err := conn.ExecContext(ctx, schema+rememberRunsSchema()); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
