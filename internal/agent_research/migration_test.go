package agent_research_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// The tables of the version before the deep-research loop state (0.179.2): no notes_id, none of the new tables. Written out here, not read
// from sql/schema.sql, so that the test starts from a database an older server made.
const oldSQLiteSchema = `
CREATE TABLE gb_agent_research_runs (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id       TEXT NOT NULL,
	parent_id     INTEGER REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	name          TEXT NOT NULL DEFAULT '',
	reason        TEXT NOT NULL DEFAULT '',
	topic         TEXT NOT NULL CHECK (topic <> ''),
	status        TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed', 'stopped')),
	state         TEXT NOT NULL DEFAULT '',
	model         TEXT NOT NULL DEFAULT '',
	max_steps     INTEGER NOT NULL DEFAULT 0,
	runner        TEXT NOT NULL DEFAULT 'local',
	agent_name    TEXT NOT NULL DEFAULT '',
	agent_version TEXT NOT NULL DEFAULT '',
	agent_hash    TEXT NOT NULL DEFAULT '',
	steps         INTEGER NOT NULL DEFAULT 0,
	tool_calls    INTEGER NOT NULL DEFAULT 0,
	input_tokens  INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	cost_usd      REAL NOT NULL DEFAULT 0,
	report        TEXT NOT NULL DEFAULT '',
	error         TEXT NOT NULL DEFAULT '',
	created_at    DATETIME NOT NULL,
	finished_at   DATETIME
);
CREATE INDEX idx_gb_agent_research_runs_user ON gb_agent_research_runs(user_id, id);
CREATE TABLE gb_agent_research_states (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id      INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	seq         INTEGER NOT NULL CHECK (seq >= 1),
	state       TEXT NOT NULL CHECK (state <> ''),
	step        INTEGER NOT NULL DEFAULT 0 CHECK (step >= 0),
	detail      TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT '',
	started_at  DATETIME NOT NULL,
	finished_at DATETIME,
	UNIQUE (run_id, seq)
);
INSERT INTO gb_agent_research_runs (user_id, topic, status, created_at) VALUES ('old-user', 'old topic', 'completed', '2025-01-01T00:00:00.000Z');
`

const oldPostgresSchema = `
CREATE TABLE gb_agent_research_runs (
	id            BIGSERIAL PRIMARY KEY,
	user_id       TEXT NOT NULL,
	parent_id     BIGINT REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	name          TEXT NOT NULL DEFAULT '',
	reason        TEXT NOT NULL DEFAULT '',
	topic         TEXT NOT NULL CHECK (topic <> ''),
	status        TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed', 'stopped')),
	state         TEXT NOT NULL DEFAULT '',
	model         TEXT NOT NULL DEFAULT '',
	max_steps     BIGINT NOT NULL DEFAULT 0,
	runner        TEXT NOT NULL DEFAULT 'local',
	agent_name    TEXT NOT NULL DEFAULT '',
	agent_version TEXT NOT NULL DEFAULT '',
	agent_hash    TEXT NOT NULL DEFAULT '',
	steps         BIGINT NOT NULL DEFAULT 0,
	tool_calls    BIGINT NOT NULL DEFAULT 0,
	input_tokens  BIGINT NOT NULL DEFAULT 0,
	output_tokens BIGINT NOT NULL DEFAULT 0,
	cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0,
	report        TEXT NOT NULL DEFAULT '',
	error         TEXT NOT NULL DEFAULT '',
	created_at    TIMESTAMPTZ NOT NULL,
	finished_at   TIMESTAMPTZ
);
CREATE TABLE gb_agent_research_states (
	id          BIGSERIAL PRIMARY KEY,
	run_id      BIGINT NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	seq         BIGINT NOT NULL CHECK (seq >= 1),
	state       TEXT NOT NULL CHECK (state <> ''),
	step        BIGINT NOT NULL DEFAULT 0 CHECK (step >= 0),
	detail      TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT '',
	started_at  TIMESTAMPTZ NOT NULL,
	finished_at TIMESTAMPTZ,
	UNIQUE (run_id, seq)
);
INSERT INTO gb_agent_research_runs (user_id, topic, status, created_at) VALUES ('old-user', 'old topic', 'completed', '2025-01-01T00:00:00Z');
`

var newTables = []string{"gb_agent_frontier_questions", "gb_agent_frontier_deps", "gb_agent_frontier_attempts", "gb_agent_frontier_transitions", "gb_agent_sources",
	"gb_agent_claims", "gb_agent_claim_sources", "gb_agent_challenges", "gb_agent_challenge_sources", "gb_agent_notes_versions"}

// oldDatabase returns a database that an older server made: the runs and states tables without notes_id and none of the new tables, with one run.
func oldDatabase(t *testing.T,
	backend string) *sql.DB {
	t.Helper()
	if backend == config.DBBackendSQLite {
		d, err := relational.Open(config.DBBackendSQLite, filepath.Join(t.TempDir(), "old.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		if _, err := d.Exec(oldSQLiteSchema); err != nil {
			t.Fatal(err)
		}
		return d
	}
	dsn := os.Getenv(dbtest.EnvPostgresDSN)
	if dsn == "" {
		t.Skipf("%s not set", dbtest.EnvPostgresDSN)
	}
	d := dbtest.OpenPostgres(t, dsn)
	drops := append(append([]string{}, newTables...),
		"gb_agent_research_states",
		"gb_agent_research_runs")
	for _, name := range drops {
		if _, err := d.Exec(`DROP TABLE IF EXISTS ` + name + ` CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(oldPostgresSchema); err != nil {
		t.Fatal(err)
	}
	return d
}

func hasColumn(t *testing.T,
	d *sql.DB,
	backend,
	table,
	column string) bool {
	t.Helper()
	q := `SELECT COUNT(*) FROM pragma_table_info('` + table + `') WHERE name = ?`
	if backend == config.DBBackendPostgres {
		q = `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = '` + table + `' AND column_name = $1`
	}
	var n int
	if err := d.QueryRow(q, column).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func hasTable(t *testing.T,
	d *sql.DB,
	backend,
	table string) bool {
	t.Helper()
	q := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
	if backend == config.DBBackendPostgres {
		q = `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1`
	}
	var n int
	if err := d.QueryRow(q, table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestMigrationAddsNotesIDAndTables(t *testing.T) {
	for _, backend := range []string{config.DBBackendSQLite, config.DBBackendPostgres} {
		t.Run(backend, func(t *testing.T) {
			testMigration(t, backend)
		})
	}
}

func testMigration(t *testing.T,
	backend string) {
	d := oldDatabase(t, backend)
	if hasColumn(t,
		d,
		backend,
		"gb_agent_research_runs",
		"notes_id") || hasTable(t,
		d,
		backend,
		"gb_agent_notes_versions") {
		t.Fatal("the old database must not have the new schema")
	}
	if err := agent_research.Open(backend, d); err != nil {
		t.Fatal(err)
	}
	if !hasColumn(t,
		d,
		backend,
		"gb_agent_research_runs",
		"notes_id") {
		t.Fatal("notes_id was not added")
	}
	for _, table := range newTables {
		if !hasTable(t,
			d,
			backend,
			table) {
			t.Errorf("table %s was not created", table)
		}
	}
	check := func() {
		t.Helper()
		var n int
		var notesID, topic string
		if err := d.QueryRow(`SELECT COUNT(*) FROM gb_agent_research_runs`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("old rows: %d %v",
				n,
				err)
		}
		if err := d.QueryRow(`SELECT notes_id, topic FROM gb_agent_research_runs`).Scan(&notesID, &topic); err != nil || notesID != "" || topic != "old topic" {
			t.Fatalf("old row: %q %q %v",
				notesID,
				topic,
				err)
		}
	}
	check()
	// A second start changes nothing and does not fail.
	if err := agent_research.Open(backend, d); err != nil {
		t.Fatalf("second start: %v", err)
	}
	check()

	// The database refuses what the constraints forbid.
	ph := func(n int) string {
		if backend == config.DBBackendPostgres {
			return "$" + string(rune('0'+n))
		}
		return "?"
	}
	var runID int64
	if err := d.QueryRow(`SELECT id FROM gb_agent_research_runs`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	now := "2025-01-01T00:00:00Z"
	exec := func(query string,
		args ...any) error {
		_, err := d.Exec(query, args...)
		return err
	}
	if err := exec(`INSERT INTO gb_agent_frontier_questions (run_id, id, depth, question, priority, created_at, updated_at) VALUES (`+ph(1)+`, 'q_001', 0, 'root', 0.5, `+ph(2)+`, `+ph(3)+`)`,
		runID,
		now,
		now); err != nil {
		t.Fatal(err)
	}
	rejected := map[string]error{
		"unknown to_status": exec(`INSERT INTO gb_agent_frontier_transitions (run_id, question_id, seq, to_status, trigger, actor, priority, created_at) VALUES (`+ph(1)+`, 'q_001', 1, 'nowhere', 'created', 'code', 0.5, `+ph(2)+`)`,
			runID,
			now),
		"unknown actor": exec(`INSERT INTO gb_agent_frontier_transitions (run_id, question_id, seq, to_status, trigger, actor, priority, created_at) VALUES (`+ph(1)+`, 'q_001', 1, 'pending', 'created', 'god', 0.5, `+ph(2)+`)`,
			runID,
			now),
		"unknown question": exec(`INSERT INTO gb_agent_frontier_transitions (run_id, question_id, seq, to_status, trigger, actor, priority, created_at) VALUES (`+ph(1)+`, 'q_099', 1, 'pending', 'created', 'code', 0.5, `+ph(2)+`)`,
			runID,
			now),
		"seq 0 version": exec(`INSERT INTO gb_agent_notes_versions (run_id, seq, text, created_at) VALUES (`+ph(1)+`, 0, 'x', `+ph(2)+`)`,
			runID,
			now),
		"version of no run": exec(`INSERT INTO gb_agent_notes_versions (run_id, seq, text, created_at) VALUES (999, 1, 'x', `+ph(1)+`)`, now),
		"bad priority": exec(`INSERT INTO gb_agent_frontier_questions (run_id, id, depth, question, priority, created_at, updated_at) VALUES (`+ph(1)+`, 'q_002', 1, 'x', 2, `+ph(2)+`, `+ph(3)+`)`,
			runID,
			now,
			now),
		"bad claim status": exec(`INSERT INTO gb_agent_claims (run_id, id, text, status, created_at, updated_at) VALUES (`+ph(1)+`, 'c_001', 'x', 'MAYBE', `+ph(2)+`, `+ph(3)+`)`,
			runID,
			now,
			now),
		"open with outcome": exec(`INSERT INTO gb_agent_challenges (run_id, id, target_type, negation, strategy, expected_if_false, query, status, outcome, created_at) VALUES (`+ph(1)+`, 'ch_001', 'claim', 'n', 'negation', 'e', 'q', 'open', 'survived', `+ph(2)+`)`,
			runID,
			now),
	}
	for name, err := range rejected {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Deleting the run deletes its rows.
	if err := exec(`DELETE FROM gb_agent_research_runs WHERE id = `+ph(1), runID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM gb_agent_frontier_questions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("questions of a deleted run: %d %v",
			n,
			err)
	}
}

// A new database gets notes_id from sql/schema.sql, and the upgrade step is a no-op.
func TestFreshDatabaseHasNotesID(t *testing.T) {
	dbtest.Run(t, func(t *testing.T,
		d *sql.DB) {
		backend := config.DBBackendSQLite
		if strings.HasSuffix(t.Name(), "postgres") {
			backend = config.DBBackendPostgres
		}
		if !hasColumn(t,
			d,
			backend,
			"gb_agent_research_runs",
			"notes_id") {
			t.Fatal("a fresh database lacks notes_id")
		}
		for _, table := range newTables {
			if !hasTable(t,
				d,
				backend,
				table) {
				t.Errorf("table %s", table)
			}
		}
		if err := agent_research.Open(backend, d); err != nil {
			t.Fatalf("the upgrade is a no-op: %v", err)
		}
	})
}
