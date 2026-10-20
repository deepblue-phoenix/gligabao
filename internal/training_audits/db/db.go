package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
)

// ErrNotFound means the record asked for (or to be updated or finished) does not exist.
var ErrNotFound = errors.New("training audits: not found")

// Execer is what *sql.DB and *sql.Tx share for running statements; the functions of the backends take it, so they can run in a
// transaction.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store is the functions of a backend as a value, so that the code that runs audits can be given either backend (sqlite.Store or
// postgres.Store) without knowing which. The functions are those of the backend packages, documented there.
type Store struct {
	CreateAuditRun       func(ctx context.Context, q Execer, r *core.AuditRun) error
	CreateAuditCaseRun   func(ctx context.Context, q Execer, c *core.AuditCaseRun) error
	UpdateAuditRun       func(ctx context.Context, q Execer, r *core.AuditRun) error
	FinishAuditRun       func(ctx context.Context, q Execer, id core.AuditRunID, status core.Status, errText string) error
	FailRunningAuditRuns func(ctx context.Context, q Execer, reason string) (int64, error)
	GetAuditRun          func(ctx context.Context, q Execer, id core.AuditRunID) (*core.AuditRun, error)
	ListAuditRuns        func(ctx context.Context, q Execer, trainedModelID int64, limit int) ([]core.AuditRun, error)
	ListAuditCaseRuns    func(ctx context.Context, q Execer, runID core.AuditRunID) ([]core.AuditCaseRun, error)
}

// ParseParams unmarshals the text of the params column into Params (nil for an empty object).
func ParseParams(text []byte) (core.Params, error) {
	if len(text) == 0 {
		return nil, nil
	}
	var p core.Params
	if err := json.Unmarshal(text, &p); err != nil {
		return nil, fmt.Errorf("unmarshal params: %w", err)
	}
	if len(p) == 0 {
		return nil, nil
	}
	return p, nil
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
