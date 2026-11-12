// Package sqlite is the sqlite storage of the GBerror (package errors): the schema (Schema) and Insert. It does not depend on package
// errors, which uses it.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"
)

// Schema is the schema of the table gb_errors for sqlite. The statements are idempotent.
//
//go:embed sql/schema.sql
var Schema string

// Execer is what *sql.DB and *sql.Tx share for running a statement that returns a row.
type Execer interface {
	QueryRowContext(ctx context.Context,
		query string,
		args ...any) *sql.Row
}

// Row is a row of gb_errors, to insert. Meta is the JSON text of the metadata object.
type Row struct {
	Op, Package, Function, Message, Stack, Meta string
	CreatedAt                                   time.Time
}

// Insert stores r into gb_errors and returns its ID.
func Insert(ctx context.Context,
	q Execer,
	r Row) (int64, error) {
	createdAt := r.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	var id int64
	if err := q.QueryRowContext(ctx,
		`
		INSERT INTO gb_errors (op, package, function, message, stack, meta, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		r.Op,
		r.Package,
		r.Function,
		r.Message,
		r.Stack,
		r.Meta,
		createdAt.UTC().Format(time.RFC3339Nano)).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert gberror %q: %w",
			r.Op,
			err)
	}
	return id, nil
}
