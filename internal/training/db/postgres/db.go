// Package postgres stores the training data (training job runs and their events, trained models, the inferences run on them,
// and the training datasets) in PostgreSQL tables and runs every SQL statement for them. The functions here are reached
// through the routing functions of package db (internal/training/db) and mirror those of package sqlite, in the PostgreSQL
// dialect ($n placeholders, RETURNING, native types).
//
// The tables live in the same database as the relational data, so this package works on the *sql.DB opened by
// internal/db/relational/postgres.
package postgres

import (
	"context"
	"database/sql"
	"time"
)

// Open applies the training schema to d, a database opened by relational/postgres.Open.
func Open(d *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return initSchema(ctx, d)
}

func utc(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time.UTC()
	return &t
}
