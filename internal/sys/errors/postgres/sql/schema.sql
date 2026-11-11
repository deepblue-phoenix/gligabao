-- errors.GBerror: the same table as in the sqlite schema (see there for the columns), with native types.
CREATE TABLE IF NOT EXISTS gb_errors (
	id         BIGSERIAL PRIMARY KEY,
	op         TEXT NOT NULL CHECK (op <> ''),
	package    TEXT NOT NULL DEFAULT '',
	function   TEXT NOT NULL DEFAULT '',
	message    TEXT NOT NULL DEFAULT '',
	stack      TEXT NOT NULL DEFAULT '',
	meta       JSONB NOT NULL DEFAULT '{}',
	created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_gb_errors_op_created ON gb_errors (op, created_at);
CREATE INDEX IF NOT EXISTS idx_gb_errors_created ON gb_errors (created_at);
