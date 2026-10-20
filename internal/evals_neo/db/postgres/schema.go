// Package postgres holds the Postgres storage of the evals data model (package core). For now it is only the schema.
package postgres

// Schema is the schema for Postgres: the same tables as the sqlite package (see there for what they hold), with native types
// (BIGSERIAL ids, JSONB for params, tags and payloads, TIMESTAMPTZ for times). The statements are idempotent and in dependency
// order, so the whole schema can be executed in one call.
const Schema = `
-- ---------------------------------------------------------------------------------------------------------------------
-- DEFINITIONS (what to run; edited and reused)
-- ---------------------------------------------------------------------------------------------------------------------

-- core.EvalDef: a named collection of cases of one type. params: JSON object; tags: JSON array of strings.
CREATE TABLE IF NOT EXISTS gb_neo_eval_defs (
	id          BIGSERIAL PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	type        TEXT NOT NULL CHECK (type <> ''),
	tags        JSONB NOT NULL DEFAULT '[]',
	params      JSONB NOT NULL DEFAULT '{}',
	created_at  TIMESTAMPTZ NOT NULL,
	updated_at  TIMESTAMPTZ NOT NULL
);

-- core.CaseDef: belongs to exactly one eval (its cases go with it); the name and the position are unique within the eval.
-- input, expected: type-specific JSON (core.Payload); expected is NULL if the case has none.
CREATE TABLE IF NOT EXISTS gb_neo_case_defs (
	id          BIGSERIAL PRIMARY KEY,
	eval_def_id BIGINT NOT NULL REFERENCES gb_neo_eval_defs(id) ON DELETE CASCADE,
	name        TEXT NOT NULL,
	position    BIGINT NOT NULL CHECK (position >= 0),
	description TEXT NOT NULL DEFAULT '',
	tags        JSONB NOT NULL DEFAULT '[]',
	input       JSONB,
	expected    JSONB,
	params      JSONB NOT NULL DEFAULT '{}',
	created_at  TIMESTAMPTZ NOT NULL,
	updated_at  TIMESTAMPTZ NOT NULL,
	UNIQUE (eval_def_id, name),
	UNIQUE (eval_def_id, position)
);

-- core.ExperimentDef: a named group of evals run together, plus the settings under test.
CREATE TABLE IF NOT EXISTS gb_neo_experiment_defs (
	id          BIGSERIAL PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	params      JSONB NOT NULL DEFAULT '{}',
	created_at  TIMESTAMPTZ NOT NULL,
	updated_at  TIMESTAMPTZ NOT NULL
);

-- core.ExperimentDef.EvalDefIDs: the evals of an experiment, in the order they run (many-to-many: an eval may be in many
-- experiments). An eval that is in an experiment cannot be deleted.
CREATE TABLE IF NOT EXISTS gb_neo_experiment_def_evals (
	experiment_def_id BIGINT NOT NULL REFERENCES gb_neo_experiment_defs(id) ON DELETE CASCADE,
	eval_def_id       BIGINT NOT NULL REFERENCES gb_neo_eval_defs(id) ON DELETE RESTRICT,
	position          BIGINT NOT NULL CHECK (position >= 0),
	PRIMARY KEY (experiment_def_id, eval_def_id),
	UNIQUE (experiment_def_id, position)
);
CREATE INDEX IF NOT EXISTS idx_gb_neo_experiment_def_evals_eval ON gb_neo_experiment_def_evals(eval_def_id);

-- ---------------------------------------------------------------------------------------------------------------------
-- RUNS (what happened; append-only history). Links go from child to parent only. A def that has runs cannot be deleted
-- (ON DELETE RESTRICT): the history must stay understandable. Deleting a run deletes the runs under it.
--   status        core.Status: the lifecycle, never a verdict. error says why it errored (status 'errored' only).
--   params        how the run was invoked (JSON object)
--   counts_*      core.Counts, over the case runs (for an experiment run, over those of all its eval runs)
--   cost_usd      LLM cost
--   created_at, started_at, finished_at   core.Timing; finished_at is set once the status is final
-- ---------------------------------------------------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS gb_neo_experiment_runs (
	id               BIGSERIAL PRIMARY KEY,
	def_id           BIGINT NOT NULL REFERENCES gb_neo_experiment_defs(id) ON DELETE RESTRICT,
	status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'errored', 'cancelled')),
	error            TEXT NOT NULL DEFAULT '',
	params           JSONB NOT NULL DEFAULT '{}',
	counts_total     BIGINT NOT NULL DEFAULT 0,
	counts_pending   BIGINT NOT NULL DEFAULT 0,
	counts_running   BIGINT NOT NULL DEFAULT 0,
	counts_completed BIGINT NOT NULL DEFAULT 0,
	counts_errored   BIGINT NOT NULL DEFAULT 0,
	counts_cancelled BIGINT NOT NULL DEFAULT 0,
	counts_passed    BIGINT NOT NULL DEFAULT 0,
	counts_failed    BIGINT NOT NULL DEFAULT 0,
	cost_usd         DOUBLE PRECISION NOT NULL DEFAULT 0,
	created_at       TIMESTAMPTZ NOT NULL,
	started_at       TIMESTAMPTZ,
	finished_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_gb_neo_experiment_runs_def ON gb_neo_experiment_runs(def_id);

-- experiment_run_id is NULL when the eval was run on its own.
CREATE TABLE IF NOT EXISTS gb_neo_eval_runs (
	id                BIGSERIAL PRIMARY KEY,
	def_id            BIGINT NOT NULL REFERENCES gb_neo_eval_defs(id) ON DELETE RESTRICT,
	experiment_run_id BIGINT REFERENCES gb_neo_experiment_runs(id) ON DELETE CASCADE,
	status            TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'errored', 'cancelled')),
	error             TEXT NOT NULL DEFAULT '',
	params            JSONB NOT NULL DEFAULT '{}',
	counts_total      BIGINT NOT NULL DEFAULT 0,
	counts_pending    BIGINT NOT NULL DEFAULT 0,
	counts_running    BIGINT NOT NULL DEFAULT 0,
	counts_completed  BIGINT NOT NULL DEFAULT 0,
	counts_errored    BIGINT NOT NULL DEFAULT 0,
	counts_cancelled  BIGINT NOT NULL DEFAULT 0,
	counts_passed     BIGINT NOT NULL DEFAULT 0,
	counts_failed     BIGINT NOT NULL DEFAULT 0,
	cost_usd          DOUBLE PRECISION NOT NULL DEFAULT 0,
	created_at        TIMESTAMPTZ NOT NULL,
	started_at        TIMESTAMPTZ,
	finished_at       TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_gb_neo_eval_runs_def ON gb_neo_eval_runs(def_id);
CREATE INDEX IF NOT EXISTS idx_gb_neo_eval_runs_experiment_run ON gb_neo_eval_runs(experiment_run_id);

-- input, output, details: type-specific JSON (core.Payload), NULL if none. input is what was actually given (the def may change
-- later). verdict (core.Verdict) is '' unless the case run completed and has one; score is in [0, 1], NULL if the case has none.
CREATE TABLE IF NOT EXISTS gb_neo_case_runs (
	id          BIGSERIAL PRIMARY KEY,
	def_id      BIGINT NOT NULL REFERENCES gb_neo_case_defs(id) ON DELETE RESTRICT,
	eval_run_id BIGINT NOT NULL REFERENCES gb_neo_eval_runs(id) ON DELETE CASCADE,
	status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'errored', 'cancelled')),
	error       TEXT NOT NULL DEFAULT '',
	input       JSONB,
	output      JSONB,
	verdict     TEXT NOT NULL DEFAULT '' CHECK (verdict IN ('', 'pass', 'fail')),
	score       DOUBLE PRECISION CHECK (score IS NULL OR (score >= 0 AND score <= 1)),
	details     JSONB,
	cost_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
	created_at  TIMESTAMPTZ NOT NULL,
	started_at  TIMESTAMPTZ,
	finished_at TIMESTAMPTZ,
	CHECK (status = 'completed' OR (verdict = '' AND score IS NULL)),
	UNIQUE (eval_run_id, def_id)
);
CREATE INDEX IF NOT EXISTS idx_gb_neo_case_runs_def ON gb_neo_case_runs(def_id);
`
