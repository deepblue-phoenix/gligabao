-- errors.GBerror: an error that is key to track, created by errors.New (internal/sys/errors). op: the symbolic name of the operation
-- that failed (one word, or several separated by '.'; for example 'remember.extract'). package, function: where it occurred (Go
-- import path, and the function in it). message: the text of the cause. stack: the stack trace, one frame per line. meta: unique
-- failure parameters and arguments that help debugging (JSON object, small). created_at: UTC RFC 3339. Append-only.
CREATE TABLE IF NOT EXISTS gb_errors (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	op         TEXT NOT NULL CHECK (op <> ''),
	package    TEXT NOT NULL DEFAULT '',
	function   TEXT NOT NULL DEFAULT '',
	message    TEXT NOT NULL DEFAULT '',
	stack      TEXT NOT NULL DEFAULT '',
	meta       TEXT NOT NULL DEFAULT '{}',
	created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_gb_errors_op_created ON gb_errors (op, created_at);
CREATE INDEX IF NOT EXISTS idx_gb_errors_created ON gb_errors (created_at);
