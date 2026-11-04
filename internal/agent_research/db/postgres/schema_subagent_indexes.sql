
CREATE INDEX IF NOT EXISTS idx_gb_agent_research_runs_parent ON gb_agent_research_runs(parent_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_agent_research_runs_parent_name ON gb_agent_research_runs(parent_id, name) WHERE parent_id IS NOT NULL;
