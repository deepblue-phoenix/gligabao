package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"

	_ "modernc.org/sqlite"
)

// A database made before the YAML/JSON ontologies (OWL tables, no ontology columns on the graph) is upgraded when it is opened: the
// OWL tables are dropped, the new ones created and the columns added.
func TestOpenUpgradesOWLDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Skip("no sqlite driver by that name:", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE gb_datasets (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', tags TEXT NOT NULL DEFAULT '[]', created_at DATETIME NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, deleted_at DATETIME)`,
		`CREATE TABLE gb_ontologies (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', iri TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', format TEXT NOT NULL, size_bytes INTEGER NOT NULL, sha256 TEXT NOT NULL, classes INTEGER NOT NULL, properties INTEGER NOT NULL, edges INTEGER NOT NULL, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, UNIQUE (user_id, name))`,
		`CREATE TABLE gb_onto_classes (id INTEGER PRIMARY KEY AUTOINCREMENT, ontology_id INTEGER NOT NULL REFERENCES gb_ontologies(id) ON DELETE CASCADE, iri TEXT NOT NULL, name TEXT NOT NULL, label TEXT NOT NULL DEFAULT '', comment TEXT NOT NULL DEFAULT '', UNIQUE (ontology_id, iri))`,
		`INSERT INTO gb_ontologies (user_id, name, format, size_bytes, sha256, classes, properties, edges, created_at, updated_at) VALUES ('u', 'm', 'rdfxml', 1, 'x', 1, 0, 0, '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z')`,
	} {
		if _, err := old.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	d, err := relational.Open(config.DBBackendSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := graph.Open(config.DBBackendSQLite, d); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('gb_ontologies') WHERE name IN ('spec', 'version')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("gb_ontologies was not recreated: %d new columns (%v)", n, err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'gb_onto_classes'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("gb_onto_classes is still there (%v)", err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM gb_ontologies`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the old ontologies were kept: %d (%v)", n, err)
	}
}

// A database whose graph tables lack the ontology columns gets them.
func TestOpenAddsOntologyColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	d, err := relational.Open(config.DBBackendSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := graph.Open(config.DBBackendSQLite, d); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"ontology_version", "ontology_class", "ontology_valid"} {
		if _, err := d.Exec(`ALTER TABLE gb_entities DROP COLUMN ` + col); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	d, err = relational.Open(config.DBBackendSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := graph.Open(config.DBBackendSQLite, d); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('gb_entities') WHERE name LIKE 'ontology_%'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("%d ontology columns, want 3 (%v)", n, err)
	}
}
