// Package postgres holds the Postgres storage of the training audits data model (package core). It has the schema (Schema) and the functions that create the records of its tables (create.go; for now only creation).
package postgres

// Schema is the schema for Postgres: the same tables as the sqlite package (see there for what they hold), with native types
// (BIGSERIAL ids, JSONB for params, TIMESTAMPTZ for times). The statements are idempotent and in dependency order, so the whole
// schema can be executed in one call.
const Schema = `
-- core.AuditRun: one execution of an audit of a trained model over one dataset. trained_model_id is not a foreign key, because the
-- trained model tables are not part of this schema. dataset: the name of the training dataset that was asked, dataset_sha256 the
-- digest of the file that was read. status is core.Status: the lifecycle, never a verdict; error says why it errored (status
-- 'errored' only). params: how the run was invoked (JSON object). counts_*: core.Counts, over the case runs. cost_usd: the judge's
-- LLM cost. The rest are core.AuditStats, the statistics of the run as a whole, rewritten after every judge batch: the questions
-- asked are the counts_total and the ones that errored its counts_errored; num_answered: the ones the model answered; num_judged:
-- the ones scored, split by the judge's score (>= 4 / 2-3 / 0-1) into num_correct, num_partial and num_incorrect; score_sum: the
-- sum of the scores (0 to 5 each) of the judged ones; num_batches: judge batches done; judge_calls: LLM calls made (a batch may
-- need a retry). created_at, started_at, finished_at: core.Timing; finished_at is set once the status is final.
CREATE TABLE IF NOT EXISTS gb_neo_training_audit_runs (
	id                  BIGSERIAL PRIMARY KEY,
	trained_model_id    BIGINT NOT NULL,
	dataset             TEXT NOT NULL CHECK (dataset <> ''),
	dataset_sha256      TEXT NOT NULL DEFAULT '',
	status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'errored', 'cancelled')),
	error               TEXT NOT NULL DEFAULT '',
	params              JSONB NOT NULL DEFAULT '{}',
	counts_total        BIGINT NOT NULL DEFAULT 0,
	counts_pending      BIGINT NOT NULL DEFAULT 0,
	counts_running      BIGINT NOT NULL DEFAULT 0,
	counts_completed    BIGINT NOT NULL DEFAULT 0,
	counts_errored      BIGINT NOT NULL DEFAULT 0,
	counts_cancelled    BIGINT NOT NULL DEFAULT 0,
	cost_usd            DOUBLE PRECISION NOT NULL DEFAULT 0,
	judge_model         TEXT NOT NULL DEFAULT '',
	num_answered        BIGINT NOT NULL DEFAULT 0,
	num_judged          BIGINT NOT NULL DEFAULT 0,
	num_correct         BIGINT NOT NULL DEFAULT 0,
	num_partial         BIGINT NOT NULL DEFAULT 0,
	num_incorrect       BIGINT NOT NULL DEFAULT 0,
	score_sum           BIGINT NOT NULL DEFAULT 0,
	num_batches         BIGINT NOT NULL DEFAULT 0,
	judge_calls         BIGINT NOT NULL DEFAULT 0,
	judge_input_tokens  BIGINT NOT NULL DEFAULT 0,
	judge_output_tokens BIGINT NOT NULL DEFAULT 0,
	created_at          TIMESTAMPTZ NOT NULL,
	started_at          TIMESTAMPTZ,
	finished_at         TIMESTAMPTZ,
	CHECK (num_correct + num_partial + num_incorrect = num_judged),
	CHECK (score_sum <= 5 * num_judged)
);
CREATE INDEX IF NOT EXISTS idx_gb_neo_training_audit_runs_model ON gb_neo_training_audit_runs(trained_model_id);

-- core.AuditCaseRun: one example of the dataset of an audit run, asked and judged. idx is its position in the examples the run was
-- given (0-based; unique within the run). instruction, input, reference are the question and the answer the model was trained to
-- give, as they were when the case ran; response is the model's ('' if it failed). judge_score is the judge's, 0 to 5 (NULL unless
-- the case completed; its score is judge_score / 5), judge_reason its explanation; batch is the judge batch, from 1 (0 if not
-- judged). input_tokens, output_tokens, duration_ms are those of the model's answer. Deleting the run deletes its case runs.
CREATE TABLE IF NOT EXISTS gb_neo_training_audit_case_runs (
	id            BIGSERIAL PRIMARY KEY,
	run_id        BIGINT NOT NULL REFERENCES gb_neo_training_audit_runs(id) ON DELETE CASCADE,
	idx           BIGINT NOT NULL CHECK (idx >= 0),
	status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'errored', 'cancelled')),
	error         TEXT NOT NULL DEFAULT '',
	instruction   TEXT NOT NULL,
	input         TEXT NOT NULL DEFAULT '',
	reference     TEXT NOT NULL DEFAULT '',
	response      TEXT NOT NULL DEFAULT '',
	batch         BIGINT NOT NULL DEFAULT 0 CHECK (batch >= 0),
	judge_score   BIGINT CHECK (judge_score IS NULL OR (judge_score >= 0 AND judge_score <= 5)),
	judge_reason  TEXT NOT NULL DEFAULT '',
	input_tokens  BIGINT NOT NULL DEFAULT 0,
	output_tokens BIGINT NOT NULL DEFAULT 0,
	duration_ms   BIGINT NOT NULL DEFAULT 0,
	created_at    TIMESTAMPTZ NOT NULL,
	CHECK (status = 'completed' OR judge_score IS NULL),
	UNIQUE (run_id, idx)
);
`
