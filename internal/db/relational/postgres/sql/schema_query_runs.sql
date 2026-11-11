
CREATE TABLE IF NOT EXISTS gb_query_runs (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	dataset     TEXT NOT NULL,
	question    TEXT NOT NULL,
	params      JSONB NOT NULL DEFAULT '{}',
	session_id  TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL CHECK (status IN ('not_started', 'started', 'succeeded', 'failed', 'skipped')),
	error       TEXT,
	started_at  TIMESTAMPTZ NOT NULL,
	finished_at TIMESTAMPTZ,
	retrieve_graph_status      TEXT NOT NULL DEFAULT 'not_started' CHECK (retrieve_graph_status IN ('not_started', 'started', 'succeeded', 'failed', 'skipped')),
	retrieve_graph_error       TEXT,
	retrieve_graph_started_at  TIMESTAMPTZ,
	retrieve_graph_finished_at TIMESTAMPTZ,
	retrieve_graph_inference_ids  JSONB NOT NULL DEFAULT '[]',
	detect_geo_status      TEXT NOT NULL DEFAULT 'not_started' CHECK (detect_geo_status IN ('not_started', 'started', 'succeeded', 'failed', 'skipped')),
	detect_geo_error       TEXT,
	detect_geo_started_at  TIMESTAMPTZ,
	detect_geo_finished_at TIMESTAMPTZ,
	detect_geo_inference_ids  JSONB NOT NULL DEFAULT '[]',
	nearby_geo_status      TEXT NOT NULL DEFAULT 'not_started' CHECK (nearby_geo_status IN ('not_started', 'started', 'succeeded', 'failed', 'skipped')),
	nearby_geo_error       TEXT,
	nearby_geo_started_at  TIMESTAMPTZ,
	nearby_geo_finished_at TIMESTAMPTZ,
	query_status      TEXT NOT NULL DEFAULT 'not_started' CHECK (query_status IN ('not_started', 'started', 'succeeded', 'failed', 'skipped')),
	query_error       TEXT,
	query_started_at  TIMESTAMPTZ,
	query_finished_at TIMESTAMPTZ,
	query_inference_ids  JSONB NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_gb_query_runs_dataset ON gb_query_runs(user_id, dataset);
CREATE INDEX IF NOT EXISTS idx_gb_query_runs_status ON gb_query_runs(status);
