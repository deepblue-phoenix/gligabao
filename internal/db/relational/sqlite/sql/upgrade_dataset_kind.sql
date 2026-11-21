-- Run once on a database whose gb_datasets has no kind column (see upgradeDatasetKind): the columns kind and eval_run_id, and the
-- unique index of the live names now per kind (schema.sql makes the new one right after).
ALTER TABLE gb_datasets ADD COLUMN kind TEXT NOT NULL DEFAULT 'data' CHECK (kind IN ('data', 'eval'));
ALTER TABLE gb_datasets ADD COLUMN eval_run_id INTEGER;
DROP INDEX IF EXISTS idx_gb_datasets_user_name;
