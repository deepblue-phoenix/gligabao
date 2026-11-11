
-- core.Run: ONE ROW PER EXECUTION OF THE DEEP-RESEARCH AGENT on a topic (see internal/agent_research and the sqlite schema).
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

-- Deep-research loop tables (same as the sqlite schema, which has the column comments).
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

CREATE TABLE IF NOT EXISTS gb_agent_frontier_deps (
	run_id        BIGINT NOT NULL,
	question_id   TEXT NOT NULL,
	depends_on_id TEXT NOT NULL,
	PRIMARY KEY (run_id, question_id, depends_on_id),
	CHECK (question_id <> depends_on_id),
	FOREIGN KEY (run_id, question_id)   REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE,
	FOREIGN KEY (run_id, depends_on_id) REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE
);

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

CREATE TABLE IF NOT EXISTS gb_agent_claim_sources (
	run_id    BIGINT NOT NULL,
	claim_id  TEXT NOT NULL,
	source_id TEXT NOT NULL,
	stance    TEXT NOT NULL CHECK (stance IN ('supports', 'contradicts')),
	PRIMARY KEY (run_id, claim_id, source_id),
	FOREIGN KEY (run_id, claim_id)  REFERENCES gb_agent_claims(run_id, id) ON DELETE CASCADE,
	FOREIGN KEY (run_id, source_id) REFERENCES gb_agent_sources(run_id, id) ON DELETE CASCADE
);

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

CREATE TABLE IF NOT EXISTS gb_agent_challenge_sources (
	run_id       BIGINT NOT NULL,
	challenge_id TEXT NOT NULL,
	source_id    TEXT NOT NULL,
	PRIMARY KEY (run_id, challenge_id, source_id),
	FOREIGN KEY (run_id, challenge_id) REFERENCES gb_agent_challenges(run_id, id) ON DELETE CASCADE,
	FOREIGN KEY (run_id, source_id)    REFERENCES gb_agent_sources(run_id, id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS gb_agent_notes_versions (
	run_id     BIGINT NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	seq        BIGINT NOT NULL CHECK (seq >= 1),
	state_seq  BIGINT NOT NULL DEFAULT 0,
	reason     TEXT NOT NULL DEFAULT '',
	text       TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (run_id, seq)
);
