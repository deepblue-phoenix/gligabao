
CREATE TABLE IF NOT EXISTS gb_datasets (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	tags        JSONB NOT NULL DEFAULT '[]',
	created_at  TIMESTAMPTZ NOT NULL,
	deleted     BOOLEAN NOT NULL DEFAULT FALSE,
	deleted_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gb_datasets_user_name
	ON gb_datasets(user_id, name)
	WHERE NOT deleted;

CREATE TABLE IF NOT EXISTS gb_ontologies (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	iri         TEXT NOT NULL DEFAULT '',
	title       TEXT NOT NULL DEFAULT '',
	format      TEXT NOT NULL,
	size_bytes  BIGINT NOT NULL,
	sha256      TEXT NOT NULL,
	classes     BIGINT NOT NULL,
	properties  BIGINT NOT NULL,
	edges       BIGINT NOT NULL,
	created_at  TIMESTAMPTZ NOT NULL,
	updated_at  TIMESTAMPTZ NOT NULL,
	UNIQUE (user_id, name)
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