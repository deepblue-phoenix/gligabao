
-- One row per training job the ML sidecar started (see sqlite/sql/schema.sql).
CREATE TABLE IF NOT EXISTS gb_training_job_runs (
	id              BIGSERIAL PRIMARY KEY,
	user_id         TEXT NOT NULL DEFAULT '',
	model_type      TEXT NOT NULL DEFAULT 'lora',
	info            TEXT NOT NULL DEFAULT '{}',
	job_id          TEXT NOT NULL,
	provider_job_id TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL,
	model_size      TEXT NOT NULL,
	dataset         TEXT NOT NULL DEFAULT '',
	num_train_steps BIGINT NOT NULL,
	last_step       BIGINT NOT NULL DEFAULT 0,
	last_loss       DOUBLE PRECISION,
	final_loss      DOUBLE PRECISION,
	error           TEXT NOT NULL DEFAULT '',
	started_at      TIMESTAMPTZ NOT NULL,
	created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
	finished_at     TIMESTAMPTZ
);
-- A database made before the runs had a user gets the column (not filled in: such runs belong to no user).
ALTER TABLE gb_training_job_runs ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE gb_training_job_runs ADD COLUMN IF NOT EXISTS model_type TEXT NOT NULL DEFAULT 'lora';
ALTER TABLE gb_training_job_runs ADD COLUMN IF NOT EXISTS info TEXT NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_gb_training_job_runs_job ON gb_training_job_runs(job_id);
CREATE INDEX IF NOT EXISTS idx_gb_training_job_runs_user ON gb_training_job_runs(user_id, id);

-- The configuration and results of a training job run, one row per run (see sqlite/sql/schema.sql).
CREATE TABLE IF NOT EXISTS gb_training_job_runs_meta (
	run_id                  BIGINT PRIMARY KEY REFERENCES gb_training_job_runs(id) ON DELETE CASCADE,
	user_id                 TEXT NOT NULL DEFAULT '',
	model_type              TEXT NOT NULL DEFAULT 'lora',
	epochs                  BIGINT,
	lr                      DOUBLE PRECISION,
	batch_size              BIGINT,
	seed                    BIGINT,
	dataset_sha256          TEXT NOT NULL DEFAULT '',
	num_train_examples      BIGINT,
	num_validation_examples BIGINT,
	validation_fraction     DOUBLE PRECISION,
	split_seed              BIGINT,
	final_val_loss          DOUBLE PRECISION,
	meta_schema             TEXT NOT NULL DEFAULT '',
	meta                    TEXT NOT NULL DEFAULT '{}',
	created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_gb_training_job_runs_meta_user ON gb_training_job_runs_meta(model_type, user_id, run_id);

-- The events the ML sidecar reported for a training job (see sqlite/sql/schema.sql).
CREATE TABLE IF NOT EXISTS gb_training_job_events (
	run_id     BIGINT NOT NULL REFERENCES gb_training_job_runs(id) ON DELETE CASCADE,
	seq        BIGINT NOT NULL,
	type       TEXT NOT NULL,
	data       TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (run_id, seq)
);

-- One row per trained model: the weights a completed training job saved (see sqlite/sql/schema.sql).
CREATE TABLE IF NOT EXISTS gb_trained_models (
	id              BIGSERIAL PRIMARY KEY,
	model_type      TEXT NOT NULL DEFAULT 'lora',
	user_id         TEXT NOT NULL DEFAULT '',
	storage         TEXT NOT NULL DEFAULT 'modal_volume',
	name            TEXT NOT NULL,
	run_id          BIGINT REFERENCES gb_training_job_runs(id) ON DELETE SET NULL,
	job_id          TEXT NOT NULL UNIQUE,
	base_model      TEXT NOT NULL DEFAULT '',
	model_size      TEXT NOT NULL,
	dataset         TEXT NOT NULL DEFAULT '',
	dataset_sha256  TEXT NOT NULL DEFAULT '',
	num_examples    BIGINT NOT NULL DEFAULT 0,
	num_train_steps BIGINT NOT NULL,
	final_loss      DOUBLE PRECISION,
	params          TEXT NOT NULL DEFAULT '{}',
	path            TEXT NOT NULL,
	files           TEXT NOT NULL DEFAULT '[]',
	size_bytes      BIGINT NOT NULL DEFAULT 0,
	created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- databases that have the table from before the model type, the user and the storage
ALTER TABLE gb_trained_models ADD COLUMN IF NOT EXISTS model_type TEXT NOT NULL DEFAULT 'lora';
ALTER TABLE gb_trained_models ADD COLUMN IF NOT EXISTS user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE gb_trained_models ADD COLUMN IF NOT EXISTS storage TEXT NOT NULL DEFAULT 'modal_volume';
CREATE INDEX IF NOT EXISTS idx_gb_trained_models_run ON gb_trained_models(run_id);
CREATE INDEX IF NOT EXISTS idx_gb_trained_models_user ON gb_trained_models(model_type, user_id, id);

-- One row per inference request to a trained model (see sqlite/sql/schema.sql).
CREATE TABLE IF NOT EXISTS gb_trained_model_inferences (
	id               BIGSERIAL PRIMARY KEY,
	trained_model_id BIGINT NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	prompt           TEXT NOT NULL,
	response         TEXT NOT NULL DEFAULT '',
	status           TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
	error            TEXT NOT NULL DEFAULT '',
	model_size       TEXT NOT NULL DEFAULT '',
	model_path       TEXT NOT NULL DEFAULT '',
	max_tokens       BIGINT NOT NULL DEFAULT 0,
	temperature      DOUBLE PRECISION,
	input_tokens     BIGINT NOT NULL DEFAULT 0,
	output_tokens    BIGINT NOT NULL DEFAULT 0,
	cold_start       BOOLEAN,
	load_seconds     DOUBLE PRECISION NOT NULL DEFAULT 0,
	duration_ms      BIGINT NOT NULL DEFAULT 0,
	created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
	completed_at     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_gb_trained_model_inferences_model ON gb_trained_model_inferences(trained_model_id, id);

-- One row per deployment of a trained model through a deployer (see sqlite/sql/schema.sql).
CREATE TABLE IF NOT EXISTS gb_trained_model_deployments (
	id               BIGSERIAL PRIMARY KEY,
	trained_model_id BIGINT NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	deployer         TEXT NOT NULL,
	options          TEXT NOT NULL DEFAULT '{}',
	status           TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
	dir              TEXT NOT NULL DEFAULT '',
	ref              TEXT NOT NULL DEFAULT '',
	details          TEXT NOT NULL DEFAULT '{}',
	error            TEXT NOT NULL DEFAULT '',
	duration_ms      BIGINT NOT NULL DEFAULT 0,
	created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
	completed_at     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_gb_trained_model_deployments_model ON gb_trained_model_deployments(trained_model_id, id);

-- JSONL training datasets found in the training datasets directory by the Go server (see sqlite/sql/schema.sql).
CREATE TABLE IF NOT EXISTS gb_training_datasets (
	id          BIGSERIAL PRIMARY KEY,
	filename    TEXT NOT NULL UNIQUE,
	size_bytes  BIGINT NOT NULL,
	num_records BIGINT NOT NULL,
	sha256      TEXT NOT NULL,
	status      TEXT NOT NULL CHECK (status IN ('stored', 'synced')),
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	synced_at   TIMESTAMPTZ
);

-- One row per training of a model that runs in the local sargo of one gligabao instance (see sqlite/sql/schema.sql).
CREATE TABLE IF NOT EXISTS gb_model_train_jobs (
	id           BIGSERIAL PRIMARY KEY,
	job_id       TEXT NOT NULL UNIQUE,
	model_type   TEXT NOT NULL,
	user_id      TEXT NOT NULL,
	instance_id  TEXT NOT NULL,
	status       TEXT NOT NULL CHECK (status IN ('running', 'done', 'failed', 'lost')),
	error        TEXT NOT NULL DEFAULT '',
	job          TEXT NOT NULL DEFAULT '{}',
	info         TEXT NOT NULL DEFAULT '{}',
	model_id     BIGINT REFERENCES gb_trained_models(id) ON DELETE SET NULL,
	started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	finished_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_model_train_jobs_running ON gb_model_train_jobs(model_type, user_id) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_gb_model_train_jobs_user ON gb_model_train_jobs(model_type, user_id, id);
