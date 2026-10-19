// Package postgres stores the evals (experiments, runs and case results of the knowledge evals and of the evals and audits of
// trained models) in PostgreSQL tables and runs every SQL statement for them. The functions here are reached through the
// routing functions of package db (internal/evals/db) and mirror those of package sqlite, in the PostgreSQL dialect ($n
// placeholders, RETURNING, native types).
//
// The tables live in the same database as the relational data (they reference its datasets and trained models), so this
// package works on the *sql.DB opened by internal/db/relational/postgres.
package postgres

import (
	"context"
	"database/sql"
	"time"
)

// Open applies the eval schema to d, a database opened by relational/postgres.Open.
func Open(d *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return initSchema(ctx, d)
}

type scanner interface{ Scan(dest ...any) error }

type rowScanner interface{ Scan(dest ...any) error }
