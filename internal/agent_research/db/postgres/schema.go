// Package postgres holds the Postgres storage of the research agents' data model (package core): the schema (Schema) and the
// functions that write and read its tables (Store).
package postgres

// Schema is the schema for Postgres: the same tables as the sqlite package (see there for what they hold), with native types
// (BIGSERIAL ids, TIMESTAMPTZ for times). The statements are idempotent and in dependency order, so the whole schema can be executed
// in one call.
const Schema = `
-- core.Run: ONE ROW PER EXECUTION OF THE DEEP-RESEARCH AGENT on a topic (see internal/agent_research and the sqlite schema).
CREATE TABLE IF NOT EXISTS gb_agent_research_runs (
	id            BIGSERIAL PRIMARY KEY,
	user_id       TEXT NOT NULL,
	parent_id     BIGINT REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	name          TEXT NOT NULL DEFAULT '',
	reason        TEXT NOT NULL DEFAULT '',
	topic         TEXT NOT NULL CHECK (topic <> ''),
	status        TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed')),
	state         TEXT NOT NULL DEFAULT '',
	model         TEXT NOT NULL DEFAULT '',
	max_steps     BIGINT NOT NULL DEFAULT 0,
	steps         BIGINT NOT NULL DEFAULT 0,
	tool_calls    BIGINT NOT NULL DEFAULT 0,
	input_tokens  BIGINT NOT NULL DEFAULT 0,
	output_tokens BIGINT NOT NULL DEFAULT 0,
	cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
	report        TEXT NOT NULL DEFAULT '',
	error         TEXT NOT NULL DEFAULT '',
	created_at    TIMESTAMPTZ NOT NULL,
	finished_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_gb_agent_research_runs_user ON gb_agent_research_runs(user_id, id);
CREATE INDEX IF NOT EXISTS idx_gb_agent_research_runs_status ON gb_agent_research_runs(status);

-- core.State: ONE ROW EACH TIME A RUN ENTERS A STATE (append-only; see the sqlite schema).
CREATE TABLE IF NOT EXISTS gb_agent_research_states (
	id          BIGSERIAL PRIMARY KEY,
	run_id      BIGINT NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	seq         BIGINT NOT NULL CHECK (seq >= 1),
	state       TEXT NOT NULL CHECK (state <> ''),
	step        BIGINT NOT NULL DEFAULT 0 CHECK (step >= 0),
	detail      TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT '',
	started_at  TIMESTAMPTZ NOT NULL,
	finished_at TIMESTAMPTZ,
	UNIQUE (run_id, seq)
);
`

// Upgrade is what a database made before sub-agents (0.129.0) lacks: the columns parent_id, name and reason of gb_agent_research_runs.
const Upgrade = `
ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS parent_id BIGINT REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE;
ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '';
`

// SubagentIndexes are the indexes on the columns of Upgrade, so that they are made after it (see the sqlite package).
const SubagentIndexes = `
CREATE INDEX IF NOT EXISTS idx_gb_agent_research_runs_parent ON gb_agent_research_runs(parent_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_agent_research_runs_parent_name ON gb_agent_research_runs(parent_id, name) WHERE parent_id IS NOT NULL;
`
