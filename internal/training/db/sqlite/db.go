// Package sqlite stores the training data (training job runs and their events, trained models, the inferences run on them,
// and the training datasets) in SQLite tables and runs every SQL statement for them. The functions here are reached through
// the routing functions of package db (internal/training/db).
//
// The tables live in the same database as the relational data, so this package works on the *sql.DB opened by
// internal/db/relational/sqlite and shares its timestamp convention.
package sqlite

import (
	"database/sql"
	"time"

	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

// Open applies the training schema to d, a database opened by relational/sqlite.Open.
func Open(d *sql.DB) error { return initSchema(d) }

func parseStoredTime(s string) (t time.Time) {
	if p := rsqlite.ParseTime(s); p != nil {
		return *p
	}
	return
}
