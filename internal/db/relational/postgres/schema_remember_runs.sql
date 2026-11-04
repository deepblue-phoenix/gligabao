
CREATE TABLE IF NOT EXISTS gb_remember_runs (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	dataset     TEXT NOT NULL,
	kind        TEXT NOT NULL,
	params      JSONB NOT NULL DEFAULT '{}',
	artifact_id BIGINT,
	status      TEXT NOT NULL CHECK (status IN ('not_started', 'started', 'succeeded', 'failed', 'skipped')),
	error       TEXT,
	started_at  TIMESTAMPTZ NOT NULL,
	finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_gb_remember_runs_dataset ON gb_remember_runs(user_id, dataset);
CREATE INDEX IF NOT EXISTS idx_gb_remember_runs_status ON gb_remember_runs(status);

CREATE TABLE IF NOT EXISTS gb_remember_run_stages (
	run_id        BIGINT NOT NULL REFERENCES gb_remember_runs(id) ON DELETE CASCADE,
	stage_type    TEXT NOT NULL,
	status        TEXT NOT NULL DEFAULT 'not_started' CHECK (status IN ('not_started', 'started', 'succeeded', 'failed', 'skipped')),
	error         TEXT,
	started_at    TIMESTAMPTZ,
	finished_at   TIMESTAMPTZ,
	inference_ids JSONB NOT NULL DEFAULT '[]',
	PRIMARY KEY (run_id, stage_type)
);
CREATE INDEX IF NOT EXISTS idx_gb_remember_run_stages_status ON gb_remember_run_stages(status);

CREATE TABLE IF NOT EXISTS gb_remember_run_stage_outputs (
	id         BIGSERIAL PRIMARY KEY,
	run_id     BIGINT NOT NULL REFERENCES gb_remember_runs(id) ON DELETE CASCADE,
	stage      TEXT NOT NULL CHECK (stage IN ('classify_complexity', 'chunk_text', 'extract_graph', 'summarize_chunk', 'load_candidates', 'resolve_edges', 'store', 'store_series', 'classify_graph', 'resolve_geo', 'web_search', 'fetch_series', 'observe_series')),
	name       TEXT NOT NULL,
	label      TEXT NOT NULL DEFAULT '',
	output     JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_gb_remember_run_stage_outputs_run ON gb_remember_run_stage_outputs(run_id, stage);
CREATE INDEX IF NOT EXISTS idx_gb_remember_run_stage_outputs_created ON gb_remember_run_stage_outputs(created_at);
