package evals_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// openPostgres returns a database in a temporary schema of POSTGRES_TEST_DSN (skipped if it is not set).
func openPostgres(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("gbevalsopen_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
	})
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func readFile(t *testing.T,
	name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hasOwnerColumn(t *testing.T,
	d *sql.DB,
	backend string) bool {
	t.Helper()
	query := `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'gb_eval_defs' AND column_name = 'user_id'`
	if backend == config.DBBackendSQLite {
		query = `SELECT COUNT(*) FROM pragma_table_info('gb_eval_defs') WHERE name = 'user_id'`
	}
	var n int
	if err := d.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// TestOpenGuard: the evals tables of the shape from before the owner exist on deployed databases. Open recreates them when they are
// all empty, and refuses (leaving them alone) when one has rows.
func TestOpenGuard(t *testing.T) {
	for _, tc := range []struct {
		backend string
		open    func(*testing.T) *sql.DB
		oldFile string
	}{
		{config.DBBackendSQLite, openSQLite, "old_schema_sqlite.sql"},
		{config.DBBackendPostgres, openPostgres, "old_schema_postgres.sql"},
	} {
		t.Run(tc.backend+"/empty", func(t *testing.T) {
			d := tc.open(t)
			if _, err := d.Exec(readFile(t, tc.oldFile)); err != nil {
				t.Fatal(err)
			}
			if hasOwnerColumn(t, d, tc.backend) {
				t.Fatal("the old schema fixture has an owner")
			}
			if err := evals.Open(tc.backend, d); err != nil {
				t.Fatalf("Open on empty old tables: %v", err)
			}
			if !hasOwnerColumn(t, d, tc.backend) {
				t.Error("the tables were not recreated with an owner")
			}
			// A second Open changes nothing.
			if err := evals.Open(tc.backend, d); err != nil {
				t.Fatalf("second Open: %v", err)
			}
		})
		t.Run(tc.backend+"/with rows", func(t *testing.T) {
			d := tc.open(t)
			if _, err := d.Exec(readFile(t, tc.oldFile)); err != nil {
				t.Fatal(err)
			}
			insert := `INSERT INTO gb_eval_defs (name, type, created_at, updated_at) VALUES ('x', 'knowledge', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`
			if _, err := d.Exec(insert); err != nil {
				t.Fatal(err)
			}
			err := evals.Open(tc.backend, d)
			if err == nil || !strings.Contains(err.Error(), "drop the gb_* evals tables by hand") {
				t.Fatalf("Open on old tables with rows: err = %v", err)
			}
			var n int
			if err := d.QueryRow(`SELECT COUNT(*) FROM gb_eval_defs`).Scan(&n); err != nil || n != 1 {
				t.Errorf("rows after the refused Open = %d, %v", n, err)
			}
			if hasOwnerColumn(t, d, tc.backend) {
				t.Error("the old tables were changed")
			}
		})
		t.Run(tc.backend+"/new database", func(t *testing.T) {
			d := tc.open(t)
			if err := evals.Open(tc.backend, d); err != nil {
				t.Fatal(err)
			}
			if !hasOwnerColumn(t, d, tc.backend) {
				t.Error("no owner column")
			}
		})
	}
}
