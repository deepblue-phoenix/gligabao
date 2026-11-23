package sqlite

import (
	"database/sql"
	_ "embed"
	"fmt"
)

//---------------------------------------------------------------------------------------------

// The training tables are created in the same database as the relational data, which is opened by
// internal/db/relational/sqlite (its timestamp convention applies: all DATETIME columns hold RFC 3339 UTC strings).
//
//go:embed sql/schema.sql
var schema string

// initSchema applies the training schema.
func initSchema(d *sql.DB) error {
	if err := addTrainedModelColumns(d); err != nil {
		return err
	}
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init training schema: %w", err)
	}
	return nil
}

// addTrainedModelColumns adds the model type, the user and the storage to gb_trained_models of a database created before them (SQLite has
// no ADD COLUMN IF NOT EXISTS). It runs before the schema, whose index on them would fail without the columns; a database without the table
// is left to the schema.
func addTrainedModelColumns(d *sql.DB) error {
	var tables int
	if err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'gb_trained_models'`).Scan(&tables); err != nil {
		return fmt.Errorf("init training schema: %w", err)
	}
	if tables == 0 {
		return nil
	}
	for _, col := range [][2]string{
		{"model_type", "TEXT NOT NULL DEFAULT 'lora'"},
		{"user_id", "TEXT NOT NULL DEFAULT ''"},
		{"storage", "TEXT NOT NULL DEFAULT 'modal_volume'"},
	} {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('gb_trained_models') WHERE name = ?`,
			col[0]).Scan(&n); err != nil {
			return fmt.Errorf("init training schema: %w", err)
		}
		if n > 0 {
			continue
		}
		if _, err := d.Exec(`ALTER TABLE gb_trained_models ADD COLUMN ` + col[0] + ` ` + col[1]); err != nil {
			return fmt.Errorf("init training schema: %w", err)
		}
	}
	return nil
}
