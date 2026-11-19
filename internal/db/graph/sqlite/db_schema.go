package sqlite

import (
	"database/sql"
	_ "embed"
	"fmt"
)

//---------------------------------------------------------------------------------------------

// The graph tables reference gb_datasets, which is created by the relational backend (internal/db/relational/sqlite),
// so that schema must be applied first.
// All DATETIME columns hold RFC 3339 UTC strings (SQLite has no real datetime type; see relational/sqlite/db_time.go).
//
//go:embed sql/schema.sql
var schema string

// initSchema applies the graph schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init graph schema: %w", err)
	}
	return addOntologyColumns(d)
}

// addOntologyColumns adds the ontology grounding columns to gb_entities and gb_edges of a database created before them (SQLite has no
// ADD COLUMN IF NOT EXISTS).
func addOntologyColumns(d *sql.DB) error {
	for _, table := range []string{"gb_entities", "gb_edges"} {
		for _, col := range [][2]string{{"ontology_version", "INTEGER"}, {"ontology_class", "TEXT"}, {"ontology_valid", "INTEGER"}, {"origin_type", "TEXT"}} {
			var n int
			if err := d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name = ?`,
				col[0]).Scan(&n); err != nil {
				return fmt.Errorf("init graph schema: %w", err)
			}
			if n > 0 {
				continue
			}
			if _, err := d.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col[0] + ` ` + col[1]); err != nil {
				return fmt.Errorf("init graph schema: %w", err)
			}
		}
	}
	return nil
}

// geoIndexSchema is the geo index of gb_entity_geo; it is only created when geo is on (EnableGeoIndex).
//
//go:embed sql/optional_geo_index.sql
var geoIndexSchema string

// EnableGeoIndex creates the geo index (an R*Tree and the triggers that keep it in step with gb_entity_geo) if it does not
// exist. It does not index the rows stored before (there is no backfill). SQLite always has R*Tree, so the result is always true.
func EnableGeoIndex(d *sql.DB) (bool, error) {
	if _, err := d.Exec(geoIndexSchema); err != nil {
		return false, fmt.Errorf("init geo index: %w", err)
	}
	return true, nil
}
