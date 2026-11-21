
CREATE TABLE IF NOT EXISTS gb_datasets (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	tags        JSONB NOT NULL DEFAULT '[]',
	created_at  TIMESTAMPTZ NOT NULL,
	deleted     BOOLEAN NOT NULL DEFAULT FALSE,
	deleted_at  TIMESTAMPTZ,
	kind        TEXT NOT NULL DEFAULT 'data' CHECK (kind IN ('data', 'eval')),
	eval_run_id BIGINT,
	CONSTRAINT gb_datasets_eval_run_check CHECK (kind = 'eval' OR eval_run_id IS NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_datasets_user_name
	ON gb_datasets(user_id, kind, name)
	WHERE NOT deleted;
CREATE INDEX IF NOT EXISTS idx_gb_datasets_eval_run
	ON gb_datasets(eval_run_id)
	WHERE eval_run_id IS NOT NULL;

-- An ontology: a typed property graph (classes, relations, properties) written in the YAML/JSON DSL of
-- internal/memory/memory_ontology/ontospec (see sqlite/sql/schema.sql). Names are unique per user.
CREATE TABLE IF NOT EXISTS gb_ontologies (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	format      TEXT NOT NULL DEFAULT 'yaml',
	version     INTEGER NOT NULL DEFAULT 1,
	spec        JSONB NOT NULL,
	size_bytes  BIGINT NOT NULL,
	sha256      TEXT NOT NULL,
	classes     BIGINT NOT NULL,
	relations   BIGINT NOT NULL,
	properties  BIGINT NOT NULL,
	created_at  TIMESTAMPTZ NOT NULL,
	updated_at  TIMESTAMPTZ NOT NULL,
	UNIQUE (user_id, name)
);

-- The history of an ontology: the document of every version, as it was sent, and the spec it parsed to.
CREATE TABLE IF NOT EXISTS gb_ontology_versions (
	id          BIGSERIAL PRIMARY KEY,
	ontology_id BIGINT NOT NULL REFERENCES gb_ontologies(id) ON DELETE CASCADE,
	version     INTEGER NOT NULL,
	format      TEXT NOT NULL,
	content     TEXT NOT NULL,
	spec        JSONB NOT NULL,
	sha256      TEXT NOT NULL,
	created_at  TIMESTAMPTZ NOT NULL,
	UNIQUE (ontology_id, version)
);

-- The AI models gligabao knows (see sqlite/sql/schema.sql).
CREATE TABLE IF NOT EXISTS gb_models (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	provider   TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL DEFAULT 'llm' CHECK (kind IN ('llm', 'embedding')),
	supported  BOOLEAN NOT NULL DEFAULT FALSE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per invocation of a model (see sqlite/sql/schema.sql).
CREATE TABLE IF NOT EXISTS gb_inferences (
	id            BIGSERIAL PRIMARY KEY,
	type          TEXT NOT NULL,
	stage         TEXT NOT NULL DEFAULT '',
	model_id      TEXT REFERENCES gb_models(id),
	reasoning     TEXT NOT NULL DEFAULT 'none' CHECK (reasoning IN ('none', 'low', 'medium', 'high')),
	input_tokens  BIGINT NOT NULL,
	output_tokens BIGINT NOT NULL,
	cost_usd      DOUBLE PRECISION NOT NULL, -- total cost of the call in USD (0 if the model's price is unknown)
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_gb_inferences_type ON gb_inferences(type);
CREATE INDEX IF NOT EXISTS idx_gb_inferences_model ON gb_inferences(model_id);