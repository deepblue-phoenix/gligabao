package sqlite

import (
	"database/sql"
	_ "embed"
	"fmt"
)

//---------------------------------------------------------------------------------------------

//go:embed schema.sql
var schema string

// initSchema applies the database schema.
func initSchema(d *sql.DB) error {
	if _, err := d.Exec(schema + rememberRunsSchema + queryRunsSchema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}
