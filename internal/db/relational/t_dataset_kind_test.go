package relational_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const oldDatasetsSQLite = `
CREATE TABLE gb_datasets (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	tags        TEXT NOT NULL DEFAULT '[]',
	created_at  DATETIME NOT NULL,
	deleted     INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
	deleted_at  DATETIME
);
CREATE UNIQUE INDEX idx_gb_datasets_user_name ON gb_datasets(user_id, name) WHERE deleted = 0;
INSERT INTO gb_datasets (user_id, name, created_at) VALUES ('u', 'old', '2024-01-01T00:00:00Z');
`

const oldDatasetsPostgres = `
CREATE TABLE gb_datasets (
	id          BIGSERIAL PRIMARY KEY,
	user_id     TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	tags        JSONB NOT NULL DEFAULT '[]',
	created_at  TIMESTAMPTZ NOT NULL,
	deleted     BOOLEAN NOT NULL DEFAULT FALSE,
	deleted_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX idx_gb_datasets_user_name ON gb_datasets(user_id, name) WHERE NOT deleted;
INSERT INTO gb_datasets (user_id, name, created_at) VALUES ('u', 'old', now());
`

// TestDatasetKindUpgrade: a database made before the eval datasets gets the columns kind and eval_run_id, keeps its datasets as
// kind 'data', and its names become unique per kind; opening it again changes nothing.
func TestDatasetKindUpgrade(t *testing.T) {
	check := func(t *testing.T,
		d *sql.DB,
		insertEval string) {
		t.Helper()
		var kind string
		if err := d.QueryRow(`SELECT kind FROM gb_datasets WHERE name = 'old'`).Scan(&kind); err != nil || kind != "data" {
			t.Fatalf("the old dataset: kind = %q, %v", kind, err)
		}
		if _, err := d.Exec(insertEval); err != nil {
			t.Fatalf("an eval dataset with the name of a data one: %v", err)
		}
		if _, err := d.Exec(strings.Replace(insertEval, "'old'", "'old2'", 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(insertEval); err == nil {
			t.Error("two live eval datasets of one user with one name")
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "t.db")
		old, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := old.Exec(oldDatasetsSQLite); err != nil {
			t.Fatal(err)
		}
		old.Close()
		for range 2 {
			d, err := relational.Open(config.DBBackendSQLite, path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if err := d.QueryRow(`SELECT 1`).Err(); err != nil {
				t.Fatal(err)
			}
		}
		d, err := relational.Open(config.DBBackendSQLite, path)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		check(t,
			d,
			`INSERT INTO gb_datasets (user_id, name, created_at, kind, eval_run_id) VALUES ('u', 'old', '2024-01-01T00:00:00Z', 'eval', 1)`)
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("POSTGRES_TEST_DSN")
		if dsn == "" {
			t.Skip("POSTGRES_TEST_DSN not set")
		}
		admin, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		schema := fmt.Sprintf("gbkind_%d", time.Now().UnixNano())
		if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
			t.Fatal(err)
		}
		defer admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		withPath := dsn + " search_path=" + schema
		if strings.Contains(dsn, "://") {
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()
			withPath = u.String()
		}
		old, err := sql.Open("pgx", withPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := old.Exec(oldDatasetsPostgres); err != nil {
			t.Fatal(err)
		}
		old.Close()
		var d *sql.DB
		for range 2 {
			if d, err = relational.Open(config.DBBackendPostgres, withPath); err != nil {
				t.Fatal(err)
			}
			defer d.Close()
		}
		check(t,
			d,
			`INSERT INTO gb_datasets (user_id, name, created_at, kind, eval_run_id) VALUES ('u', 'old', now(), 'eval', 1)`)
		if _, err := d.Exec(`INSERT INTO gb_datasets (user_id, name, created_at, kind, eval_run_id) VALUES ('u', 'x', now(), 'data', 1)`); err == nil {
			t.Error("a data dataset with an eval run")
		}
	})
}
