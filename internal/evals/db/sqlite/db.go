// Package sqlite stores the evals (experiments, runs and case results of the knowledge evals and of the evals and audits of
// trained models) in SQLite tables and runs every SQL statement for them. The functions here are reached through the routing
// functions of package db (internal/evals/db).
//
// The tables live in the same database as the relational data (they reference its datasets and trained models), so this
// package works on the *sql.DB opened by internal/db/relational/sqlite and shares its timestamp convention.
package sqlite

import (
	"database/sql"
)

// Open applies the eval schema to d, a database opened by relational/sqlite.Open.
func Open(d *sql.DB) error { return initSchema(d) }

type scanner interface{ Scan(dest ...any) error }

type rowScanner interface{ Scan(dest ...any) error }
