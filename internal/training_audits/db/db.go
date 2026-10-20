package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Execer is what *sql.DB and *sql.Tx share for running statements; the create functions of the backends take it, so they can
// run in a transaction.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// JSON marshals v as the text of a JSON column. A nil value (nil slice, nil map, nil pointer) is empty, which is the text the
// column holds for "nothing" (for example "[]" for a list, "{}" for an object).
func JSON(v any, empty string) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal json: %w", err)
	}
	if string(b) == "null" {
		return empty, nil
	}
	return string(b), nil
}
