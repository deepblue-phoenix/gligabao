// Package sqlite is the sqlite storage of the GBerror (package errors): the schema (Schema) and Insert.
package sqlite

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	gberrors "github.com/deepblue-phoenix/gligabao/internal/sys/errors"
)

// Schema is the schema of the table gb_errors for sqlite. The statements are idempotent.
//
//go:embed sql/schema.sql
var Schema string

// Insert stores e into gb_errors and sets its ID. A zero CreatedAt is set to now.
func Insert(ctx context.Context, q gberrors.Execer, e *gberrors.GBerror) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	meta, err := json.Marshal(e.Meta)
	if err != nil || string(meta) == "null" {
		meta = []byte("{}")
		if err != nil {
			meta = []byte(`{"meta_error":"not encodable"}`)
		}
	}
	var id int64
	if err := q.QueryRowContext(ctx, `
		INSERT INTO gb_errors (op, package, function, message, stack, meta, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		e.Op, e.Package, e.Function, e.Message, e.Stack, string(meta), e.CreatedAt.UTC().Format(time.RFC3339Nano)).Scan(&id); err != nil {
		return fmt.Errorf("insert gberror %q: %w", e.Op, err)
	}
	e.ID = id
	return nil
}
