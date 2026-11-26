
-- Training jobs started on the ML sidecar (internal/sys/ml_sidecar, ml_sidecar/ai_train): ONE ROW PER JOB the ML sidecar reported as started,
-- made from the TrainingJob it returned from POST /training/job_start.
--   user_id          the user that started the job (the user of the request); the job, its model and everything made from them are theirs only
--   job_id           the ML sidecar's ID of the job (names its progress file there); provider_job_id is the compute provider's
--   status           'started' when the ML sidecar reports the job started, then the job's status events ('running', 'training'),
--                    finally 'completed' or 'failed'
--   model_size       '4b', '12b' or '31b'
--   dataset          the file on the ML sidecar's data volume the job trains on ('' only in a job started before a dataset was required)
--   num_train_steps  how many steps the job was asked to train
--   last_step, last_loss  from the newest progress event that has a loss; final_loss: the mean loss, when the job completed
--   error            why the job failed
--   started_at       when the ML sidecar says the job started; created_at is when the row was made; finished_at when it ended
CREATE TABLE IF NOT EXISTS gb_training_job_runs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id         TEXT NOT NULL DEFAULT '',
	model_type      TEXT NOT NULL DEFAULT 'lora',
	info            TEXT NOT NULL DEFAULT '{}',
	job_id          TEXT NOT NULL,
	provider_job_id TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL,
	model_size      TEXT NOT NULL,
	dataset         TEXT NOT NULL DEFAULT '',
	num_train_steps INTEGER NOT NULL,
	last_step       INTEGER NOT NULL DEFAULT 0,
	last_loss       REAL,
	final_loss      REAL,
	error           TEXT NOT NULL DEFAULT '',
	started_at      DATETIME NOT NULL,
	created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	finished_at     DATETIME
);
CREATE INDEX IF NOT EXISTS idx_gb_training_job_runs_job ON gb_training_job_runs(job_id);
CREATE INDEX IF NOT EXISTS idx_gb_training_job_runs_user ON gb_training_job_runs(user_id, id);

-- The configuration and results of a training job run: ONE ROW PER RUN, made when the run starts and completed when it ends. gb_training_job_runs
-- is the job (identity, status, progress, the backend's bookkeeping in info); this is the training itself, which is meant to grow. What every
-- kind of model has is a typed column; what only one kind has is the JSON meta, told apart by meta_schema.
--   run_id           the run (gb_training_job_runs); the row is deleted with it
--   user_id, model_type  copied from the run, to filter without a join
--   epochs, lr, batch_size, seed   hyperparameters (NULL: the model has none, or the default was used and is not known); seed is a 64 bit seed as int64
--   dataset_sha256   the digest of the file the job read ('' if none)
--   num_train_examples, num_validation_examples   the size of each set (graphs for GCNII*); validation_fraction and split_seed how it was split
--   final_val_loss   the loss on the validation set, when the run ended (the training loss is final_loss of the run)
--   meta_schema      the shape of meta, '<model type>.v<version>' (e.g. 'gcnii.v1'); meta a JSON object: the settings and results of one kind of model
--                    (GCNII*: hidden, depth, the class selection, the classes and the counts per class, the scores; see internal/training/gcnii/meta.go)
CREATE TABLE IF NOT EXISTS gb_training_job_runs_meta (
	run_id                  INTEGER PRIMARY KEY REFERENCES gb_training_job_runs(id) ON DELETE CASCADE,
	user_id                 TEXT NOT NULL DEFAULT '',
	model_type              TEXT NOT NULL DEFAULT 'lora',
	epochs                  INTEGER,
	lr                      REAL,
	batch_size              INTEGER,
	seed                    INTEGER,
	dataset_sha256          TEXT NOT NULL DEFAULT '',
	num_train_examples      INTEGER,
	num_validation_examples INTEGER,
	validation_fraction     REAL,
	split_seed              INTEGER,
	final_val_loss          REAL,
	meta_schema             TEXT NOT NULL DEFAULT '',
	meta                    TEXT NOT NULL DEFAULT '{}',
	created_at              DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	updated_at              DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_gb_training_job_runs_meta_user ON gb_training_job_runs_meta(model_type, user_id, run_id);

-- The events the ML sidecar reported for a training job (status changes and progress with the loss), in the order of the
-- ml_sidecar's progress file: seq is the event's index there, so a reconnect can resume after the last seq stored.
--   data: the event as JSON (internal/sys/ml_sidecar: TrainingEvent)
CREATE TABLE IF NOT EXISTS gb_training_job_events (
	run_id     INTEGER NOT NULL REFERENCES gb_training_job_runs(id) ON DELETE CASCADE,
	seq        INTEGER NOT NULL,
	type       TEXT NOT NULL,
	data       TEXT NOT NULL,
	created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	PRIMARY KEY (run_id, seq)
);

-- Trained models: ONE ROW PER COMPLETED TRAINING JOB, the set of weights the job saved: on the ML sidecar's models volume (a LoRA
-- adapter: a few files) or, for the GCNII* models of sargo, in S3. Training again never replaces a model, it makes a new row; any row
-- can be picked to run inference on. A LoRA row is made from the job's "completed" event (internal/training/monitor.go), a GCNII*
-- row when sargo's training of a user is done (internal/api/model/weights.go).
--   model_type       'lora' (the ML sidecar's adapters, also every row made before types existed) or 'gcnii' (a GCNII* graph classifier)
--   user_id          the user a model belongs to: the user of the request that trained it (for a LoRA model, the user of the job run)
--   storage          where the weights are: 'modal_volume' (ml_sidecar), 's3' (the weights bucket) or 'local' (a directory of the server)
--   run_id           the job run (gb_training_job_runs) that made it; NULL if that run was deleted. job_id is unique: a job makes one model
--   name             for lists; made from the settings when the row is made
--   base_model       the model that was fine-tuned (e.g. google/gemma-4-E4B-it); model_size '4b', '12b' or '31b' (inference needs it);
--                    both '' for a GCNII* model
--                    (GCNII*: dataset is the dataset of the user it was trained on, num_examples its training graphs, num_train_steps its epochs,
--                    final_loss the loss on the training graphs, params the other settings and the scores, see internal/api/model/weights.go)
--   dataset          the dataset file name ('' = the ML sidecar's default); dataset_sha256 the digest of the file the job read; num_examples its examples
--   num_train_steps, final_loss  how long the job trained and its mean training loss
--   params           JSON object: the other training settings (LoRA rank and alpha, learning rate, batch size, ...)
--   path             the directory of the weights in storage: on the models volume, relative to it ('trained/<job id>'), inference's model_path;
--                    in S3 the key of the directory ('model_weights/gcnii/<user>/<job id>', see config model_weights_s3_prefix)
--   files            JSON array of {path, size_bytes, sha256}: the files in that directory; size_bytes their total
CREATE TABLE IF NOT EXISTS gb_trained_models (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	model_type      TEXT NOT NULL DEFAULT 'lora',
	user_id         TEXT NOT NULL DEFAULT '',
	storage         TEXT NOT NULL DEFAULT 'modal_volume',
	name            TEXT NOT NULL,
	run_id          INTEGER REFERENCES gb_training_job_runs(id) ON DELETE SET NULL,
	job_id          TEXT NOT NULL UNIQUE,
	base_model      TEXT NOT NULL DEFAULT '',
	model_size      TEXT NOT NULL,
	dataset         TEXT NOT NULL DEFAULT '',
	dataset_sha256  TEXT NOT NULL DEFAULT '',
	num_examples    INTEGER NOT NULL DEFAULT 0,
	num_train_steps INTEGER NOT NULL,
	final_loss      REAL,
	params          TEXT NOT NULL DEFAULT '{}',
	path            TEXT NOT NULL,
	files           TEXT NOT NULL DEFAULT '[]',
	size_bytes      INTEGER NOT NULL DEFAULT 0,
	created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_gb_trained_models_run ON gb_trained_models(run_id);
CREATE INDEX IF NOT EXISTS idx_gb_trained_models_user ON gb_trained_models(model_type, user_id, id);

-- Inference requests to trained models: ONE ROW PER REQUEST to run a prompt through a trained model (internal/training/inference.go).
-- The row is made when the request starts (status 'running') and updated when the ML sidecar answers or fails, so every request can be
-- reviewed afterwards, failed ones too. A row still 'running' long after its start was interrupted (e.g. the server restarted).
--   trained_model_id  the model (gb_trained_models) the prompt was run through
--   prompt, response  what was sent and the model's reply ('' until completed)
--   status            'running', 'completed' or 'failed'; error is the reason of a failure
--   model_size, model_path  what the ML sidecar was asked to load (copied from the trained model)
--   max_tokens        0 = the ML sidecar's default; temperature NULL = the ML sidecar's default
--   input_tokens, output_tokens  token counts reported by the model (the prompt as tokenized, and what it generated); 0 if unknown
--   cold_start        1 if the request had to load the model into a GPU container, 0 if it found it loaded, NULL if unknown
--   load_seconds      how long that load took (0 when not a cold start or unknown)
--   duration_ms       how long the request took; completed_at when it ended (NULL while running)
CREATE TABLE IF NOT EXISTS gb_trained_model_inferences (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	trained_model_id INTEGER NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	prompt           TEXT NOT NULL,
	response         TEXT NOT NULL DEFAULT '',
	status           TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
	error            TEXT NOT NULL DEFAULT '',
	model_size       TEXT NOT NULL DEFAULT '',
	model_path       TEXT NOT NULL DEFAULT '',
	max_tokens       INTEGER NOT NULL DEFAULT 0,
	temperature      REAL,
	input_tokens     INTEGER NOT NULL DEFAULT 0,
	output_tokens    INTEGER NOT NULL DEFAULT 0,
	cold_start       INTEGER,
	load_seconds     REAL NOT NULL DEFAULT 0,
	duration_ms      INTEGER NOT NULL DEFAULT 0,
	created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	completed_at     DATETIME
);
CREATE INDEX IF NOT EXISTS idx_gb_trained_model_inferences_model ON gb_trained_model_inferences(trained_model_id, id);

-- Deployments of trained models: ONE ROW PER DEPLOYMENT of a trained model through a deployer, a hook of the embedding program
-- (internal/training/deploy.go). The row is made when the deployment starts (status 'running') and updated when it ends, so every
-- deployment can be reviewed afterwards, failed ones too. A row still 'running' long after its start was interrupted (e.g. the server restarted).
--   trained_model_id  the model (gb_trained_models) that was deployed
--   deployer, options the name of the deployer and the free-form options (a JSON object) the request gave it
--   status            'running', 'completed' or 'failed'; error is the reason of a failure
--   dir               the local directory the weights were fetched to and the deployer was given ('' until they are fetched)
--   ref, details      the deployer's identifier of the result and what else it reported (a JSON object); '' and '{}' until completed
--   duration_ms       how long the deployment took; completed_at when it ended (NULL while running)
CREATE TABLE IF NOT EXISTS gb_trained_model_deployments (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	trained_model_id INTEGER NOT NULL REFERENCES gb_trained_models(id) ON DELETE CASCADE,
	deployer         TEXT NOT NULL,
	options          TEXT NOT NULL DEFAULT '{}',
	status           TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
	dir              TEXT NOT NULL DEFAULT '',
	ref              TEXT NOT NULL DEFAULT '',
	details          TEXT NOT NULL DEFAULT '{}',
	error            TEXT NOT NULL DEFAULT '',
	duration_ms      INTEGER NOT NULL DEFAULT 0,
	created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	completed_at     DATETIME
);
CREATE INDEX IF NOT EXISTS idx_gb_trained_model_deployments_model ON gb_trained_model_deployments(trained_model_id, id);

-- JSONL training datasets (internal/training): the datasets are subdirectories of the training datasets directory
-- that have a dataset.toml manifest; this table has ONE ROW PER DATASET, made when the Go server first sees the data file
-- and replaced when it sees it changed. The ML sidecar is not involved, it gets the file when a training job that uses it
-- is started.
--   filename     the dataset's name plus ".jsonl" (the file name it has on the ML sidecar), unique
--   size_bytes, num_records (non-empty lines), sha256 (hex) of the file
--   status       'stored' (only here) or 'synced' (the ML sidecar has this version of the file)
--   updated_at   when the row last described a new version of the file; synced_at when the ML sidecar got it (NULL while it has not)
CREATE TABLE IF NOT EXISTS gb_training_datasets (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	filename    TEXT NOT NULL UNIQUE,
	size_bytes  INTEGER NOT NULL,
	num_records INTEGER NOT NULL,
	sha256      TEXT NOT NULL,
	status      TEXT NOT NULL CHECK (status IN ('stored', 'synced')),
	created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	synced_at   DATETIME
);

-- Trainings of models that run outside the ML sidecar: ONE ROW PER TRAINING in the local sargo of one gligabao instance (today the GCNII*
-- models, internal/api/model/weights.go). The instances of a cluster share this database but not sargo, so the row is how every instance
-- sees a training that another one runs. The instance that runs it follows sargo, beats (heartbeat_at) and, when sargo is done, stores the
-- weights and records the model (gb_trained_models), then ends the row. A running row without a recent beat is marked 'lost' by whichever
-- instance looks next (the weights were only on that instance's disk, and sargo trains in memory: a crash loses the job).
--   job_id       the id of the model it makes (gb_trained_models.job_id), chosen when it starts
--   model_type, user_id  what is trained and for whom; at most ONE 'running' row per (model_type, user_id) (the unique index below), which is how
--                a second training of the same user is refused on every instance
--   instance_id  the gligabao process that runs it
--   status       'running', 'done', 'failed' or 'lost'; error is the reason of a failure
--   job          JSON, the last state of the job as sargo reported it; info JSON, what the request knew (dataset, history run, settings)
--   model_id     the trained model of a done job (NULL otherwise, or if the model row was deleted)
--   started_at, heartbeat_at, finished_at (NULL while running)
CREATE TABLE IF NOT EXISTS gb_model_train_jobs (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id       TEXT NOT NULL UNIQUE,
	model_type   TEXT NOT NULL,
	user_id      TEXT NOT NULL,
	instance_id  TEXT NOT NULL,
	status       TEXT NOT NULL CHECK (status IN ('running', 'done', 'failed', 'lost')),
	error        TEXT NOT NULL DEFAULT '',
	job          TEXT NOT NULL DEFAULT '{}',
	info         TEXT NOT NULL DEFAULT '{}',
	model_id     INTEGER REFERENCES gb_trained_models(id) ON DELETE SET NULL,
	started_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	heartbeat_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	finished_at  DATETIME
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_model_train_jobs_running ON gb_model_train_jobs(model_type, user_id) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_gb_model_train_jobs_user ON gb_model_train_jobs(model_type, user_id, id);
