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
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("init training schema: %w", err)
	}
	return nil
}
