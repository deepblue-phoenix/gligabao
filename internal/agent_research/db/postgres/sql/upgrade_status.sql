
ALTER TABLE gb_agent_research_runs DROP CONSTRAINT IF EXISTS gb_agent_research_runs_status_check;
ALTER TABLE gb_agent_research_runs ADD CONSTRAINT gb_agent_research_runs_status_check CHECK (status IN ('running', 'completed', 'failed', 'stopped'));
