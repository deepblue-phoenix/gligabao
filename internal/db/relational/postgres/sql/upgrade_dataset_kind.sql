-- Run once on a database whose gb_datasets has no kind column (see upgradeDatasetKind): the columns kind and eval_run_id (with the
-- check that only eval datasets belong to an eval run), and the unique index of the live names now per kind (schema.sql makes the new
-- one right after).
ALTER TABLE gb_datasets ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'data' CHECK (kind IN ('data', 'eval'));
ALTER TABLE gb_datasets ADD COLUMN IF NOT EXISTS eval_run_id BIGINT;
ALTER TABLE gb_datasets ADD CONSTRAINT gb_datasets_eval_run_check CHECK (kind = 'eval' OR eval_run_id IS NULL);
DROP INDEX IF EXISTS idx_gb_datasets_user_name;
