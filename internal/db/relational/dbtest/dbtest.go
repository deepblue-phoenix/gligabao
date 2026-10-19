// Package dbtest runs tests against every available database backend (relational, graph, training and eval tables).
//
// SQLite is always used (a fresh database file per test). PostgreSQL is used too when the environment variable
// POSTGRES_TEST_DSN names a database (e.g. postgres://postgres:pw@127.0.0.1:5432/gb?sslmode=disable); each test
// then gets its own temporary schema in it, dropped afterwards, so tests are isolated and can start from empty tables.
package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// EnvPostgresDSN is the environment variable that enables the PostgreSQL runs.
const EnvPostgresDSN = "POSTGRES_TEST_DSN"

var counter atomic.Int64

// Run calls fn as a subtest once per backend ("sqlite", and "postgres" if POSTGRES_TEST_DSN is set), each time with
// a freshly opened, empty database. The database is closed (and, for postgres, its schema dropped) afterwards.
// Subtests run sequentially, because packages relational, graph, training/db and evals/db route to the backend of the last Open.
func Run(t *testing.T, fn func(t *testing.T, d *sql.DB)) {
	t.Helper()
	t.Run(config.DBBackendSQLite, func(t *testing.T) { fn(t, OpenSQLite(t)) })
	t.Run(config.DBBackendPostgres, func(t *testing.T) {
		dsn := os.Getenv(EnvPostgresDSN)
		if dsn == "" {
			t.Skipf("%s not set", EnvPostgresDSN)
		}
		fn(t, OpenPostgres(t, dsn))
	})
}

// OpenSQLite opens an empty SQLite database in a temporary directory.
func OpenSQLite(t *testing.T) *sql.DB {
	t.Helper()
	d, err := relational.Open(config.DBBackendSQLite, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := graph.Open(config.DBBackendSQLite, d); err != nil {
		d.Close()
		t.Fatal(err)
	}
	if err := trainingdb.Open(config.DBBackendSQLite, d); err != nil {
		d.Close()
		t.Fatal(err)
	}
	if err := evalsdb.Open(config.DBBackendSQLite, d); err != nil {
		d.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// OpenPostgres opens an empty PostgreSQL database: a new schema in the database at dsn, which is dropped when the
// test ends.
func OpenPostgres(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("gbtest_%d_%d", time.Now().UnixNano(), counter.Add(1))
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	d, err := relational.Open(config.DBBackendPostgres, withSearchPath(dsn, schema))
	if err == nil {
		if err = graph.Open(config.DBBackendPostgres, d); err == nil {
			if err = trainingdb.Open(config.DBBackendPostgres, d); err == nil {
				err = evalsdb.Open(config.DBBackendPostgres, d)
			}
		}
		if err != nil {
			d.Close()
		}
	}
	if err != nil {
		admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Close()
		admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
	return d
}

// withSearchPath returns dsn (URL or key=value form) with the connection's search_path set to schema.
func withSearchPath(dsn, schema string) string {
	if strings.Contains(dsn, "://") {
		if u, err := url.Parse(dsn); err == nil {
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return dsn + " search_path=" + schema
}
