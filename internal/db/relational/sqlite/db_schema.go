package sqlite

import (
	"database/sql"
	_ "embed"
	"fmt"
)

//---------------------------------------------------------------------------------------------

//go:embed sql/schema.sql
var schema string

//go:embed sql/upgrade_dataset_kind.sql
var upgradeDatasetKindSQL string

// initSchema applies the database schema.
func initSchema(d *sql.DB) error {
	if err := dropOWLOntologies(d); err != nil {
		return err
	}
	if err := upgradeDatasetKind(d); err != nil {
		return err
	}
	if _, err := d.Exec(schema + rememberRunsSchema + queryRunsSchema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}

// dropOWLOntologies removes the tables of the OWL ontologies (gb_ontologies with an "iri" column, gb_onto_classes, gb_onto_edges),
// which the YAML/JSON ontologies replace. The old ontologies cannot be converted: they are loaded again in the new format.
func dropOWLOntologies(d *sql.DB) error {
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('gb_ontologies') WHERE name = 'iri'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	for _, table := range []string{"gb_onto_edges", "gb_onto_classes", "gb_ontologies"} {
		if _, err := d.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
			return fmt.Errorf("drop %s: %w", table, err)
		}
	}
	return nil
}

// upgradeDatasetKind adds the columns kind and eval_run_id to the gb_datasets of a database made before the eval datasets, and drops
// the unique index of the names, which schema.sql makes again per kind. It does nothing on a new database (no table) or an upgraded one.
func upgradeDatasetKind(d *sql.DB) error {
	var cols, kind int
	if err := d.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN name = 'kind' THEN 1 ELSE 0 END), 0) FROM pragma_table_info('gb_datasets')`).Scan(&cols, &kind); err != nil {
		return fmt.Errorf("check gb_datasets.kind: %w", err)
	}
	if cols == 0 || kind > 0 {
		return nil
	}
	if _, err := d.Exec(upgradeDatasetKindSQL); err != nil {
		return fmt.Errorf("upgrade gb_datasets (kind): %w", err)
	}
	return nil
}
