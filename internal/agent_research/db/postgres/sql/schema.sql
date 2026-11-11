
-- gb_agent_research_runs: ONE ROW PER EXECUTION OF THE DEEP-RESEARCH AGENT (core.Run). A research a user starts is a run; so is each
-- sub-agent (worker) the investigative loop starts for a question. Column-by-column notes: the sqlite schema.
--   What it is for: who asked, on what topic, with which agent definition and model, where it runs, its status, what it has spent
--                   so far, and its outcome (the report or the error).
--   What it enables: listing and following agents by user, status and parent; stopping one; the caps on steps and cost (enforced from
--                   these counters); the sub-agent tree (parent_id); recovery after a crash (a run left 'running' is failed at start).
--   Main users: agent_research (Manager and researcher: manager.go, researcher.go, and the investigative loop in investigative.go);
--               workers.go (sub-agents); internal/api/research (REST /v1/gb/research); internal/api/admin/research.go (admin UI:
--               list, details, stop); internal/db/dbimport (imports the runs of an agent that ran in Modal).
CREATE TABLE IF NOT EXISTS gb_agent_research_runs (
	id            BIGSERIAL PRIMARY KEY,
	user_id       TEXT NOT NULL,
	parent_id     BIGINT REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	name          TEXT NOT NULL DEFAULT '',
	reason        TEXT NOT NULL DEFAULT '',
	topic         TEXT NOT NULL CHECK (topic <> ''),
	status        TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed', 'stopped')),
	state         TEXT NOT NULL DEFAULT '',
	model         TEXT NOT NULL DEFAULT '',
	max_steps     BIGINT NOT NULL DEFAULT 0,
	runner        TEXT NOT NULL DEFAULT 'local',
	agent_name    TEXT NOT NULL DEFAULT '',
	agent_version TEXT NOT NULL DEFAULT '',
	agent_hash    TEXT NOT NULL DEFAULT '',
	notes_id      TEXT NOT NULL DEFAULT '',
	phase         TEXT NOT NULL DEFAULT '',
	stop_reason   TEXT NOT NULL DEFAULT '',
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

-- gb_agent_research_states: ONE ROW EACH TIME A RUN ENTERS A STATE (core.State): the timeline of an agent. Append-only; column notes
-- in the sqlite schema.
--   What it is for: the step-by-step history of a run (what it was doing, on which step, with which detail, and where it failed).
--   What it enables: the progress view that follows a run while it works (the states of GET /v1/gb/research/{id}); the admin UI
--                    'failed' view (the state it failed in); the 'waiting for workers' and 'delegating' view of investigative runs; the
--                    timing of each stage (started_at, finished_at).
--   Main users: researcher.go (EnterState, CloseState, in one transaction per event); manager.go (ListStates for the details);
--               internal/api/research and internal/api/admin/research.go (the States section); internal/db/dbimport (imports the states
--               of Modal runs).
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

-- Deep-research loop tables: the same tables as in the sqlite schema, which has the column-by-column notes.

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
	run_id              BIGINT NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id                  TEXT NOT NULL,
	parent_id           TEXT,
	depth               BIGINT NOT NULL CHECK (depth >= 0),
	kind                TEXT NOT NULL DEFAULT 'question' CHECK (kind IN ('question', 'hypothesis')),
	question            TEXT NOT NULL CHECK (question <> ''),
	rationale           TEXT NOT NULL DEFAULT '',
	priority            DOUBLE PRECISION NOT NULL CHECK (priority >= 0 AND priority <= 1),
	status              TEXT NOT NULL DEFAULT 'pending'
	                    CHECK (status IN ('pending', 'in_progress', 'resolved', 'blocked', 'unresolvable', 'pruned')),
	validation_criteria TEXT NOT NULL DEFAULT '',
	target_sources      TEXT NOT NULL DEFAULT '[]',
	answer              TEXT NOT NULL DEFAULT '',
	claimed_by          BIGINT REFERENCES gb_agent_research_runs(id) ON DELETE SET NULL,
	created_at          TIMESTAMPTZ NOT NULL,
	updated_at          TIMESTAMPTZ NOT NULL,
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
	run_id        BIGINT NOT NULL,
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
	id          BIGSERIAL PRIMARY KEY,
	run_id      BIGINT NOT NULL,
	question_id TEXT NOT NULL,
	seq         BIGINT NOT NULL CHECK (seq >= 1),
	query       TEXT NOT NULL CHECK (query <> ''),
	tool        TEXT NOT NULL DEFAULT '',
	outcome     TEXT NOT NULL DEFAULT '',
	created_at  TIMESTAMPTZ NOT NULL,
	UNIQUE (run_id, question_id, seq),
	FOREIGN KEY (run_id, question_id) REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE
);

-- gb_agent_frontier_transitions: THE AUDIT TRAIL OF THE FRONTIER. Append-only; each row is one status change (or priority change) of a
-- question, with what caused it and who decided it. It answers "why is this question in this state" and lets the whole search be replayed.
--   What it enables: reconstructing the history of the research (what was tried, what was pruned and why, which contradiction reopened
--                    which question); the temporal and investigation views; debugging the loop's decisions.
--   Main users: investigate_core/frontier.go (builds each transition; the constructors accept only the closed trigger vocabulary);
--               investigate_store.go (InsertTransition, in the same transaction as the question's status update); investigate/loop.go.
CREATE TABLE IF NOT EXISTS gb_agent_frontier_transitions (
	id          BIGSERIAL PRIMARY KEY,
	run_id      BIGINT NOT NULL,
	question_id TEXT NOT NULL,
	seq         BIGINT NOT NULL CHECK (seq >= 1),
	from_status TEXT NOT NULL DEFAULT '',
	to_status   TEXT NOT NULL CHECK (to_status IN ('pending', 'in_progress', 'resolved', 'blocked', 'unresolvable', 'pruned')),
	trigger     TEXT NOT NULL CHECK (trigger <> ''),
	actor       TEXT NOT NULL CHECK (actor IN ('agent', 'code')),
	reason      TEXT NOT NULL DEFAULT '',
	priority    DOUBLE PRECISION NOT NULL CHECK (priority >= 0 AND priority <= 1),
	attempts    BIGINT NOT NULL DEFAULT 0,
	caused_by   TEXT NOT NULL DEFAULT '',
	state_seq   BIGINT NOT NULL DEFAULT 0,
	created_at  TIMESTAMPTZ NOT NULL,
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
	run_id     BIGINT NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id         TEXT NOT NULL,
	url        TEXT NOT NULL,
	domain     TEXT NOT NULL,
	title      TEXT NOT NULL DEFAULT '',
	note       TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (run_id, id),
	UNIQUE (run_id, url)
);

-- gb_agent_claims: the claims (statements of fact) and the numeric datapoints (kind 'datapoint' fills value and unit) the agent
-- extracts from its sources, with their verification status.
--   What it is for: the findings of the run. The report is written from the claims that survived verification.
--   What it enables: the verification workflow (UNVERIFIED -> VERIFIED, or REFUTED / RETRACTED when a challenge or a contradiction
--                    defeats it); the 'Verified', 'Unverified', 'Datapoints' and 'Contradictions' sections of the notes; the exploration
--                    gate (every resolved question must be backed by a VERIFIED claim, or a stated reason).
--   Main users: investigate_core/notes.go and tools.go (notes_append, notes_update); the 'verifying' state of the loop; finish_tools.go
--               and gates.go (the exploration gate); investigate_store.go (UpsertClaim).
CREATE TABLE IF NOT EXISTS gb_agent_claims (
	run_id      BIGINT NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id          TEXT NOT NULL,
	question_id TEXT,
	kind        TEXT NOT NULL DEFAULT 'claim' CHECK (kind IN ('claim', 'datapoint')),
	text        TEXT NOT NULL CHECK (text <> ''),
	value       TEXT NOT NULL DEFAULT '',
	unit        TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'UNVERIFIED' CHECK (status IN ('UNVERIFIED', 'VERIFIED', 'REFUTED', 'RETRACTED')),
	created_at  TIMESTAMPTZ NOT NULL,
	updated_at  TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (run_id, id)
);

-- gb_agent_claim_sources: the links between a claim and the sources that bear on it. stance says whether the source supports the
-- claim or contradicts it.
--   What it enables: the verdict of a claim is based on its evidence (supporting sources to verify it, a contradicting one to raise a
--                    contradiction); the notes show which sources back each claim.
--   Main users: investigate_core/notes.go (notes_append with a claim's sources); investigate_store.go (UpsertClaim).
CREATE TABLE IF NOT EXISTS gb_agent_claim_sources (
	run_id    BIGINT NOT NULL,
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
CREATE TABLE IF NOT EXISTS gb_agent_challenges (
	run_id            BIGINT NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id                TEXT NOT NULL,
	question_id       TEXT,
	target_type       TEXT NOT NULL CHECK (target_type IN ('claim', 'question_answer', 'conclusion')),
	target_id         TEXT NOT NULL DEFAULT '',
	negation          TEXT NOT NULL CHECK (negation <> ''),
	strategy          TEXT NOT NULL CHECK (strategy IN ('negation', 'discrepancy', 'criticism', 'alternative_method', 'scope_limit', 'primary_source', 'staleness')),
	expected_if_false TEXT NOT NULL CHECK (expected_if_false <> ''),
	query             TEXT NOT NULL CHECK (query <> ''),
	attempt_id        BIGINT REFERENCES gb_agent_frontier_attempts(id) ON DELETE SET NULL,
	status            TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
	outcome           TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'survived', 'weakened', 'refuted', 'inconclusive')),
	evidence          TEXT NOT NULL DEFAULT '',
	result_claim_id   TEXT,
	created_at        TIMESTAMPTZ NOT NULL,
	closed_at         TIMESTAMPTZ,
	PRIMARY KEY (run_id, id),
	CHECK ((status = 'open' AND outcome = '') OR (status = 'closed' AND outcome <> ''))
);
CREATE INDEX IF NOT EXISTS idx_gb_agent_challenges_target ON gb_agent_challenges(run_id, target_type, target_id);

-- gb_agent_challenge_sources: the sources that backed the verdict of a challenge (a challenge can cite several).
--   What it enables: the evidence of a verdict can be read back (the admin UI and the notes show what a refutation rests on).
--   Main users: investigate_core/challenges.go (record_challenge_result); investigate_store.go (UpsertChallenge).
CREATE TABLE IF NOT EXISTS gb_agent_challenge_sources (
	run_id       BIGINT NOT NULL,
	challenge_id TEXT NOT NULL,
	source_id    TEXT NOT NULL,
	PRIMARY KEY (run_id, challenge_id, source_id),
	FOREIGN KEY (run_id, challenge_id) REFERENCES gb_agent_challenges(run_id, id) ON DELETE CASCADE,
	FOREIGN KEY (run_id, source_id)    REFERENCES gb_agent_sources(run_id, id) ON DELETE CASCADE
);

-- gb_agent_notes_versions: the working memory of the investigative loop, as history. notes.md (the file) is the working copy the
-- model reads and writes; this table keeps every version (APPEND-ONLY).
--   What it enables: restoring the notes when the file is missing or differs from the latest version (reason 'restore'); the
--                    'latest notes' view in the admin UI; debugging and exporting what the agent knew at each turn.
--   Main users: investigate/loop.go (writes a version after each notes write, reads the latest on start or restore);
--               investigate_store.go (AddNotesVersion, LatestNotesVersion); investigate/notes_s3.go mirrors the file to the bucket.
CREATE TABLE IF NOT EXISTS gb_agent_notes_versions (
	run_id     BIGINT NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	seq        BIGINT NOT NULL CHECK (seq >= 1),
	state_seq  BIGINT NOT NULL DEFAULT 0,
	reason     TEXT NOT NULL DEFAULT '',
	text       TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (run_id, seq)
);
