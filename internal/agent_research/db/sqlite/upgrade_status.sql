
CREATE TABLE gb_agent_research_runs_new (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id       TEXT NOT NULL,
	parent_id     INTEGER REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	name          TEXT NOT NULL DEFAULT '',
	reason        TEXT NOT NULL DEFAULT '',
	topic         TEXT NOT NULL CHECK (topic <> ''),
	status        TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed', 'stopped')),
	state         TEXT NOT NULL DEFAULT '',
	model         TEXT NOT NULL DEFAULT '',
	max_steps     INTEGER NOT NULL DEFAULT 0,
	runner        TEXT NOT NULL DEFAULT 'local',
	agent_name    TEXT NOT NULL DEFAULT '',
	agent_version TEXT NOT NULL DEFAULT '',
	agent_hash    TEXT NOT NULL DEFAULT '',
	steps         INTEGER NOT NULL DEFAULT 0,
	tool_calls    INTEGER NOT NULL DEFAULT 0,
	input_tokens  INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	cost_usd      REAL NOT NULL DEFAULT 0,
	report        TEXT NOT NULL DEFAULT '',
	error         TEXT NOT NULL DEFAULT '',
	created_at    DATETIME NOT NULL,
	finished_at   DATETIME
);
INSERT INTO gb_agent_research_runs_new (id, user_id, parent_id, name, reason, topic, status, state, model, max_steps, runner, agent_name, agent_version, agent_hash,
	steps, tool_calls, input_tokens, output_tokens, cost_usd, report, error, created_at, finished_at)
SELECT id, user_id, parent_id, name, reason, topic, status, state, model, max_steps, runner, agent_name, agent_version, agent_hash,
	steps, tool_calls, input_tokens, output_tokens, cost_usd, report, error, created_at, finished_at FROM gb_agent_research_runs;
DROP TABLE gb_agent_research_runs;
ALTER TABLE gb_agent_research_runs_new RENAME TO gb_agent_research_runs;
