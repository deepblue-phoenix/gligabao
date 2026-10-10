// Package db opens the database selected in the config and delegates to the backend subpackage
// (currently only sqlite; see internal/db/sqlite). Each backend owns its schema and its SQL dialect specifics.
package db

import (
	"database/sql"
	"fmt"

	"github.com/ivan/gligabao/internal/config"
	"github.com/ivan/gligabao/internal/db/sqlite"
)

//---------------------------------------------------------------------------------------------

// Open opens the database of the given backend (config.DBBackendSQLite; empty means sqlite) and applies its
// schema. For sqlite, dsn is the database file path.
func Open(backend, dsn string) (*sql.DB, error) {
	switch backend {
	case "", config.DBBackendSQLite:
		return sqlite.Open(dsn)
	default:
		return nil, fmt.Errorf("unknown db backend %q", backend)
	}
}
