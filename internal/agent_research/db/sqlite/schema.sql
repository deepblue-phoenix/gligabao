
-- core.Run: ONE ROW PER EXECUTION OF THE DEEP-RESEARCH AGENT on a topic (see internal/agent_research).
--   user_id, topic  who asked, and what the agent researches (for a sub-agent: the sub-task it was given)
--   parent_id, name for a sub-agent, the run of the agent that spawned it and the name that agent gave it (unique among the
--                   sub-agents of one parent); NULL and '' for a research started by a user.
--   reason          for a sub-agent, why its parent spawned it (the reason it submitted with the spawn_subagent call; its siblings
--                   can read it); '' for a research started by a user Deleting the parent deletes its
--                   sub-agents.
--   status          the lifecycle: 'running', then 'completed' (it ended with a report), 'failed' (error says why) or 'stopped'
--                   (somebody stopped it; error says who). A row that stays 'running' belongs to a process that died; the server
--                   marks those 'failed' when it starts.
--   state           the state the run is in now: the state of its last row in gb_agent_research_states (core.State*)
--   model, max_steps  the model the agent runs on ('' for the server's default) and the most model calls it may make
--   runner          where the agent runs: 'local' (in the server's process) or 'modal' (the gligabao_agent binary in a Modal container)
--   agent_name, agent_version, agent_hash  the agent definition (package agent_defs) the agent was built from: its name and version, and
--                   the SHA-256 of its content (a file can change without a version bump); '' for a run made before definitions existed
--   steps, tool_calls, input_tokens, output_tokens, cost_usd  what it has spent so far, updated as it goes; the cost includes the
--                   model calls of the web searches
--   report          the answer of the agent, set when it completes
--   error           why it failed, or who stopped it; '' if it did not fail
--   created_at, finished_at  when it started and ended (finished_at is NULL while it runs)
CREATE TABLE IF NOT EXISTS gb_agent_research_runs (
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
CREATE INDEX IF NOT EXISTS idx_gb_agent_research_runs_user ON gb_agent_research_runs(user_id, id);
CREATE INDEX IF NOT EXISTS idx_gb_agent_research_runs_status ON gb_agent_research_runs(status);

-- core.State: ONE ROW EACH TIME A RUN ENTERS A STATE, the detailed history of how it went (APPEND-ONLY: a state is never rewritten
-- but for its end). Deleting the run deletes its states.
--   run_id       the run (gb_agent_research_runs)
--   seq          1, 2, 3, ... in the order the states were entered (unique within the run)
--   state        what the agent was doing: 'thinking', 'searching', 'reading', 'querying', 'remembering', 'using_tool', 'writing_report', 'completed' or
--                'failed' (core.State*); not constrained, so that an agent with other tools can have its own
--   step         the model call the agent was on (from 1; 0 for the state a run starts in or ends in)
--   detail       what the state is about: the query searched, the URL read, the tool used, 'step 3 of 20'
--   error        why the work of the state failed ('' if not); a failed tool is a failed state, not a failed run
--   started_at, finished_at  when the state was entered and left (finished_at is NULL for the state the run is in; the terminal
--                states are finished as they are entered)
CREATE TABLE IF NOT EXISTS gb_agent_research_states (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id      INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	seq         INTEGER NOT NULL CHECK (seq >= 1),
	state       TEXT NOT NULL CHECK (state <> ''),
	step        INTEGER NOT NULL DEFAULT 0 CHECK (step >= 0),
	detail      TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT '',
	started_at  DATETIME NOT NULL,
	finished_at DATETIME,
	UNIQUE (run_id, seq)
);
