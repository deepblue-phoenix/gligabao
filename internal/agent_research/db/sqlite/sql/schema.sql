
-- gb_agent_research_runs: ONE ROW PER EXECUTION OF THE DEEP-RESEARCH AGENT (core.Run). A research a user starts is a run; so is each
-- sub-agent (worker) the investigative loop starts for a question.
--   What it is for: who asked, on what topic, with which agent definition and model, where it runs, its status, what it has spent
--                   so far, and its outcome (the report or the error).
--   What it enables: listing and following agents by user, status and parent; stopping one; the caps on steps and cost (enforced from
--                   these counters); the sub-agent tree (parent_id); recovery after a crash (a run left 'running' is failed at start).
--   Main users: agent_research (Manager and researcher: manager.go, researcher.go, and the investigative loop in investigative.go);
--               workers.go (sub-agents); internal/api/research (REST /v1/gb/research); internal/api/admin/research.go (admin UI:
--               list, details, stop); internal/db/dbimport (imports the runs of an agent that ran in Modal).
-- The columns, one by one:
--   user_id, topic  who asked, and what the agent researches (for a worker: its brief)
--   parent_id, name for a worker (a sub-agent of the investigative loop), the run of the agent that started it and its name (the id of
--                   its question; unique among the workers of one parent); NULL and '' for a research started by a user. (Runs of
--                   sub-agents that the model spawned, before 0.189.0, look the same.)
--   reason          for a worker, why its supervisor started it ('question q_005: ...'); '' for a research started by a user.
--                   Deleting the parent deletes its workers.
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
	notes_id      TEXT NOT NULL DEFAULT '',
	phase         TEXT NOT NULL DEFAULT '',
	stop_reason   TEXT NOT NULL DEFAULT '',
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

-- gb_agent_research_states: ONE ROW EACH TIME A RUN ENTERS A STATE (core.State): the timeline of an agent.
--   What it is for: the step-by-step history of a run (what it was doing, on which step, with which detail, and where it failed).
--   What it enables: the progress view that follows a run while it works (the states of GET /v1/gb/research/{id}); the admin UI
--                    'failed' view (the state it failed in); the 'waiting for workers' and 'delegating' view of investigative runs; the
--                    timing of each stage (started_at, finished_at).
--   Main users: researcher.go (EnterState, CloseState, in one transaction per event); manager.go (ListStates for the details);
--               internal/api/research and internal/api/admin/research.go (the States section); internal/db/dbimport (imports the states
--               of Modal runs).
-- The columns, one by one (APPEND-ONLY: a state is never rewritten
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

-- ---- Deep-research loop (docs/agents/deep-research-loop.md): frontier, claims and sources, challenges, notes versions. ----

-- gb_agent_frontier_questions: ONE ROW PER QUESTION of the investigative loop's frontier, a node of the question graph of a run.
--   What it is for: the plan of the research. Each question has a parent, a depth, a priority, a status and, once settled, its answer.
--   What it enables: the loop picks the next question to work on (by status, priority and depth); it resumes exactly where it stopped;
--                    it shows the tree of questions to the model (frontier_view) and in the admin UI (Investigation block); it keeps
--                    the root question ('q_001') and the sub-questions a contradiction or a hypothesis spawned.
--   Main users: investigate_core/frontier.go (the rules: pick, branch, prune, unblock); investigate_core/tools.go (add_question,
--               retry_question, resolve_question, reopen_question, set_priority, prune_branch, frontier_next, frontier_view);
--               investigate/loop.go and db/*/investigate_store.go (Load, UpsertQuestion); the delegation to workers ('delegated' transitions in delegate.go).
-- Its history of status changes is in gb_agent_frontier_transitions.
CREATE TABLE IF NOT EXISTS gb_agent_frontier_questions (
	run_id              INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id                  TEXT NOT NULL,
	parent_id           TEXT,
	depth               INTEGER NOT NULL CHECK (depth >= 0),
	kind                TEXT NOT NULL DEFAULT 'question' CHECK (kind IN ('question', 'hypothesis')),
	question            TEXT NOT NULL CHECK (question <> ''),
	rationale           TEXT NOT NULL DEFAULT '',
	priority            REAL NOT NULL CHECK (priority >= 0 AND priority <= 1),
	status              TEXT NOT NULL DEFAULT 'pending'
	                    CHECK (status IN ('pending', 'in_progress', 'resolved', 'blocked', 'unresolvable', 'pruned')),
	validation_criteria TEXT NOT NULL DEFAULT '',
	target_sources      TEXT NOT NULL DEFAULT '[]',   -- JSON array of strings
	answer              TEXT NOT NULL DEFAULT '',     -- resolved: the answer; unresolvable: the assumption; pruned: the reason
	claimed_by          INTEGER REFERENCES gb_agent_research_runs(id) ON DELETE SET NULL, -- worker run while in_progress
	created_at          DATETIME NOT NULL,
	updated_at          DATETIME NOT NULL,
	PRIMARY KEY (run_id, id),
	FOREIGN KEY (run_id, parent_id) REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_agent_frontier_questions_pick
	ON gb_agent_frontier_questions(run_id, status, priority DESC, depth);

-- gb_agent_frontier_deps: the edges "question depends on depends_on_id" of the frontier (a DAG; the code rejects cycles before insert).
--   What it enables: a question waits for the questions it depends on. When one is resolved the dependent is unblocked
--                    ('dependency_met' transition); when one fails it may be marked blocked ('dependency_failed').
--   Main users: investigate_core/frontier.go (the rules that block and unblock questions); investigate_store.go (AddDependency).
CREATE TABLE IF NOT EXISTS gb_agent_frontier_deps (
	run_id        INTEGER NOT NULL,
	question_id   TEXT NOT NULL,
	depends_on_id TEXT NOT NULL,
	PRIMARY KEY (run_id, question_id, depends_on_id),
	CHECK (question_id <> depends_on_id),
	FOREIGN KEY (run_id, question_id)   REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE,
	FOREIGN KEY (run_id, depends_on_id) REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE
);

-- gb_agent_frontier_attempts: APPEND-ONLY log of the queries tried on a question (one row per try: a web search, a page read or a
-- knowledge-base query).
--   What it enables: the cap on attempts per question (MaxAttempts): when it is reached the question becomes unresolvable; a retried
--                    query must differ from the earlier ones; the link from a challenge to the search that ran it (gb_agent_challenges.attempt_id).
--   Main users: investigate_core/frontier.go (AddAttempt; retry_question in tools.go); investigate_core/challenges.go (the search of a
--               challenge is an attempt); investigate_core/diff.go and delegate.go (the InsertAttempt effect, also for worker findings);
--               investigate_store.go (InsertAttempt).
CREATE TABLE IF NOT EXISTS gb_agent_frontier_attempts (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,   -- BIGSERIAL in PostgreSQL
	run_id      INTEGER NOT NULL,
	question_id TEXT NOT NULL,
	seq         INTEGER NOT NULL CHECK (seq >= 1),   -- 1, 2, ... per question
	query       TEXT NOT NULL CHECK (query <> ''),
	tool        TEXT NOT NULL DEFAULT '',            -- web_search, fetch_page, query (a challenge row points to its attempt)
	outcome     TEXT NOT NULL DEFAULT '',            -- one-line summary of what it yielded
	created_at  DATETIME NOT NULL,
	UNIQUE (run_id, question_id, seq),
	FOREIGN KEY (run_id, question_id) REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE
);

-- gb_agent_frontier_transitions: THE AUDIT TRAIL OF THE FRONTIER. Each row is one status change (or priority change) of a question, with
-- what caused it and who decided it. It answers "why is this question in this state" and lets the whole search be replayed.
--   What it enables: reconstructing the history of the research (what was tried, what was pruned and why, which contradiction reopened
--                    which question); the temporal and investigation views; debugging the loop's decisions.
--   Main users: investigate_core/frontier.go (builds each transition; the constructors accept only the closed trigger vocabulary);
--               investigate_store.go (InsertTransition, in the same transaction as the question's status update); investigate/loop.go.
-- APPEND-ONLY: ONE ROW PER STATUS CHANGE OF A QUESTION (never updated, never deleted but with the run). The current status stays in
-- gb_agent_frontier_questions.status; the code writes that column and the transition row in the SAME transaction, so the two never disagree
-- and the latest row of a question always equals its status. Replaying the rows of a run rebuilds the history of the whole search.
--   question_id    the question (gb_agent_frontier_questions.id, same run)
--   seq            1, 2, 3, ... per question (unique with the question): the order of its changes; seq 1 is the creation (from_status NULL)
--   from_status    the status before ('' on creation)
--   to_status      the status after: pending, in_progress, resolved, blocked, unresolvable or pruned
--   trigger        what caused it, a closed vocabulary: created, selected, resolved, retried, attempts_exhausted, reopened, dependency_failed,
--                  dependency_met, reprioritized (from_status = to_status), pruned_branch, pruned_depth, pruned_scope, contradiction,
--                  delegated (a question handed to a worker: pending to in_progress, by code)
--   actor          'agent' (a tool call of the model) or 'code' (a deterministic rule: attempt cap, branch pruning, unblocking)
--   reason         the agent's stated reason, or the rule that fired (for resolved: the answer's gist; for unresolvable: the assumption)
--   priority       the question's priority at that moment (so re-prioritization is visible between rows)
--   attempts       attempts made so far
--   caused_by      the question or challenge that triggered it, if any ('q_044', 'ch_003'): e.g. a contradiction spawned q_044, which reopened q_012
--   state_seq      the research state (gb_agent_research_states.seq of the run) in which it happened, to tie it to the step/turn
--   created_at     when it happened
CREATE TABLE IF NOT EXISTS gb_agent_frontier_transitions (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,   -- BIGSERIAL in PostgreSQL
	run_id      INTEGER NOT NULL,
	question_id TEXT NOT NULL,
	seq         INTEGER NOT NULL CHECK (seq >= 1),
	from_status TEXT NOT NULL DEFAULT '',
	to_status   TEXT NOT NULL CHECK (to_status IN ('pending', 'in_progress', 'resolved', 'blocked', 'unresolvable', 'pruned')),
	trigger     TEXT NOT NULL CHECK (trigger <> ''),
	actor       TEXT NOT NULL CHECK (actor IN ('agent', 'code')),
	reason      TEXT NOT NULL DEFAULT '',
	priority    REAL NOT NULL CHECK (priority >= 0 AND priority <= 1),
	attempts    INTEGER NOT NULL DEFAULT 0,
	caused_by   TEXT NOT NULL DEFAULT '',
	state_seq   INTEGER NOT NULL DEFAULT 0,
	created_at  DATETIME NOT NULL,
	UNIQUE (run_id, question_id, seq),
	FOREIGN KEY (run_id, question_id) REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_agent_frontier_transitions_run ON gb_agent_frontier_transitions(run_id, id);

-- gb_agent_sources: the sources read in a run, one row per distinct URL (id 's_001', ...).
--   What it is for: the evidence base. Claims and challenges cite sources by id.
--   What it enables: the 'Sources' section of the notes; the rule that a question is only settled with enough sources from enough
--                    distinct domains (domain is the registrable domain, used for that count); the provenance of each claim.
--   Main users: investigate_store.go (UpsertSource); investigate_core/notes.go (the Sources section, notes_append); gates.go (the
--               exploration gate counts the domains); the web tools (web_search, fetch_page) that record what they read.
CREATE TABLE IF NOT EXISTS gb_agent_sources (
	run_id     INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id         TEXT NOT NULL,                        -- 's_001'
	url        TEXT NOT NULL,
	domain     TEXT NOT NULL,                        -- registrable domain, for the independence check
	title      TEXT NOT NULL DEFAULT '',
	note       TEXT NOT NULL DEFAULT '',             -- e.g. 'read, nothing relevant'
	created_at DATETIME NOT NULL,
	PRIMARY KEY (run_id, id),
	UNIQUE (run_id, url)
);

-- gb_agent_claims: the claims (statements of fact) and the numeric datapoints (kind 'datapoint' fills value and unit) the agent extracts
-- from its sources, with their verification status.
--   What it is for: the findings of the run. The report is written from the claims that survived verification.
--   What it enables: the verification workflow (UNVERIFIED -> VERIFIED, or REFUTED / RETRACTED when a challenge or a contradiction
--                    defeats it); the 'Verified', 'Unverified', 'Datapoints' and 'Contradictions' sections of the notes; the exploration
--                    gate (every resolved question must be backed by a VERIFIED claim, or a stated reason).
--   Main users: investigate_core/notes.go and tools.go (notes_append, notes_update); the 'verifying' state of the loop; finish_tools.go
--               and gates.go (the exploration gate); investigate_store.go (UpsertClaim).
CREATE TABLE IF NOT EXISTS gb_agent_claims (
	run_id      INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id          TEXT NOT NULL,                       -- 'c_001', 'd_001'
	question_id TEXT,                                -- the question that produced it
	kind        TEXT NOT NULL DEFAULT 'claim' CHECK (kind IN ('claim', 'datapoint')),
	text        TEXT NOT NULL CHECK (text <> ''),
	value       TEXT NOT NULL DEFAULT '',
	unit        TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'UNVERIFIED' CHECK (status IN ('UNVERIFIED', 'VERIFIED', 'REFUTED', 'RETRACTED')),
	created_at  DATETIME NOT NULL,
	updated_at  DATETIME NOT NULL,
	PRIMARY KEY (run_id, id)
);

-- gb_agent_claim_sources: the links between a claim and the sources that bear on it. stance says whether the source supports the
--   claim or contradicts it.
--   What it enables: the verdict of a claim is based on its evidence (supporting sources to verify it, a contradicting one to raise a
--                    contradiction); the notes show which sources back each claim.
--   Main users: investigate_core/notes.go (notes_append with a claim's sources); investigate_store.go (UpsertClaim).
CREATE TABLE IF NOT EXISTS gb_agent_claim_sources (
	run_id    INTEGER NOT NULL,
	claim_id  TEXT NOT NULL,
	source_id TEXT NOT NULL,
	stance    TEXT NOT NULL CHECK (stance IN ('supports', 'contradicts')),
	PRIMARY KEY (run_id, claim_id, source_id),
	FOREIGN KEY (run_id, claim_id)  REFERENCES gb_agent_claims(run_id, id) ON DELETE CASCADE,
	FOREIGN KEY (run_id, source_id) REFERENCES gb_agent_sources(run_id, id) ON DELETE CASCADE
);

-- gb_agent_challenges: ONE ROW PER ADVERSARIAL QUERY. It is how the agent tries to break its own claims, answers and conclusion.
--   What it is for: a claim or an answer is only trusted after the agent has searched for evidence against it and found none (or
--                   found it and weakened or refuted the claim).
--   What it enables: the 'verifying' and 'challenging' states of the loop; the 'no open challenge' rule of the exploration gate; the
--                    'Challenges' view in the admin UI (target, strategy, negation, outcome, evidence); the audit of what was tried against a
--                    conclusion before the report.
--   Main users: investigate_core/challenges.go and tools.go (challenge creates a row, record_challenge_result closes it); finish_tools.go
--               and gates.go (the conclusion must have survived a challenge); investigate_store.go (UpsertChallenge, loadChallenges).
-- ONE ROW PER ADVERSARIAL QUERY: how the agent attacks its own claims, answers and conclusion. Created by the 'challenge' tool,
-- closed by 'record_challenge_result'. Rows are never deleted but with the run; the verdict fields are set once, on closing.
--   target_type  'claim', 'question_answer' or 'conclusion'
--   target_id    the claim id (claim) or the question id (question_answer); '' for the conclusion
--   negation     the belief inverted into the statement the search tries to prove
--   strategy     negation, discrepancy, criticism, alternative_method, scope_limit, primary_source or staleness
--   expected_if_false  what the evidence would look like if the belief were wrong (written BEFORE the search)
--   query        the search text that was run
--   attempt_id   the row of gb_agent_frontier_attempts that executed it (NULL until it runs)
--   status       'open' until a verdict is recorded, then 'closed'
--   outcome      survived, weakened, refuted or inconclusive ('' while open)
--   evidence     what was found, short; required for weakened and refuted
--   result_claim_id  the claim the counter-evidence produced, if any
CREATE TABLE IF NOT EXISTS gb_agent_challenges (
	run_id            INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id                TEXT NOT NULL,                  -- 'ch_001', per run
	question_id       TEXT,                           -- the question being worked on when it was raised
	target_type       TEXT NOT NULL CHECK (target_type IN ('claim', 'question_answer', 'conclusion')),
	target_id         TEXT NOT NULL DEFAULT '',
	negation          TEXT NOT NULL CHECK (negation <> ''),
	strategy          TEXT NOT NULL CHECK (strategy IN ('negation', 'discrepancy', 'criticism', 'alternative_method', 'scope_limit', 'primary_source', 'staleness')),
	expected_if_false TEXT NOT NULL CHECK (expected_if_false <> ''),
	query             TEXT NOT NULL CHECK (query <> ''),
	attempt_id        INTEGER REFERENCES gb_agent_frontier_attempts(id) ON DELETE SET NULL,
	status            TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
	outcome           TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'survived', 'weakened', 'refuted', 'inconclusive')),
	evidence          TEXT NOT NULL DEFAULT '',
	result_claim_id   TEXT,
	created_at        DATETIME NOT NULL,
	closed_at         DATETIME,
	PRIMARY KEY (run_id, id),
	CHECK ((status = 'open' AND outcome = '') OR (status = 'closed' AND outcome <> ''))
);
CREATE INDEX IF NOT EXISTS idx_gb_agent_challenges_target ON gb_agent_challenges(run_id, target_type, target_id);

-- gb_agent_challenge_sources: the sources that backed the verdict of a challenge (a challenge can cite several).
--   What it enables: the evidence of a verdict can be read back (the admin UI and the notes show what a refutation rests on).
--   Main users: investigate_core/challenges.go (record_challenge_result); investigate_store.go (UpsertChallenge).
CREATE TABLE IF NOT EXISTS gb_agent_challenge_sources (
	run_id       INTEGER NOT NULL,
	challenge_id TEXT NOT NULL,
	source_id    TEXT NOT NULL,
	PRIMARY KEY (run_id, challenge_id, source_id),
	FOREIGN KEY (run_id, challenge_id) REFERENCES gb_agent_challenges(run_id, id) ON DELETE CASCADE,
	FOREIGN KEY (run_id, source_id)    REFERENCES gb_agent_sources(run_id, id) ON DELETE CASCADE
);

-- gb_agent_notes_versions: the working memory of the investigative loop, as history. notes.md (the file) is the working copy the model
-- reads and writes; this table keeps every version.
--   What it enables: restoring the notes when the file is missing or differs from the latest version (reason 'restore'); the
--                    'latest notes' view in the admin UI; debugging and exporting what the agent knew at each turn.
--   Main users: investigate/loop.go (writes a version after each notes write, reads the latest on start or restore);
--               investigate_store.go (AddNotesVersion, LatestNotesVersion); investigate/notes_s3.go mirrors the file to the bucket.
-- APPEND-ONLY: the whole notes.md after each write, for debugging and export (the file is the working copy; this is the record).
--   seq        1, 2, 3, ... per run, in write order
--   state_seq  gb_agent_research_states.seq of the run when the write happened, to tie it to the turn
--   reason     what wrote it: notes_append, notes_update, notes_scratch, consolidation, restore (the loop restored the file)
--   text       the whole notes.md after the write
CREATE TABLE IF NOT EXISTS gb_agent_notes_versions (
	run_id     INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	seq        INTEGER NOT NULL CHECK (seq >= 1),
	state_seq  INTEGER NOT NULL DEFAULT 0,
	reason     TEXT NOT NULL DEFAULT '',
	text       TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	PRIMARY KEY (run_id, seq)
);
