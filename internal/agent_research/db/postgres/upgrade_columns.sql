
ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS parent_id BIGINT REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE;
ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '';
ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS runner TEXT NOT NULL DEFAULT 'local';
ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS agent_name TEXT NOT NULL DEFAULT '';
ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS agent_version TEXT NOT NULL DEFAULT '';
ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS agent_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS notes_id TEXT NOT NULL DEFAULT '';
