package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/core"
)

// ErrNotFound means the record asked for (or to be updated) does not exist.
var ErrNotFound = errors.New("evals: not found")

// Execer is what *sql.DB and *sql.Tx share for running statements; the functions of the backends take it, so they can run in a
// transaction.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store is the functions of a backend as a value, so that the code that runs evals can be given either backend (sqlite.Store or
// postgres.Store) without knowing which. The functions are those of the backend packages, documented there. The ones that take
// a Meta are for every Type: they pick the table by the Meta's type.
type Store struct {
	// Definitions.
	CreateEvalDef       func(ctx context.Context, q Execer, d *core.EvalDef) error
	UpdateEvalDef       func(ctx context.Context, q Execer, d *core.EvalDef) error
	GetEvalDefByName    func(ctx context.Context, q Execer, name string) (*core.EvalDef, error)
	CreateCaseDef       func(ctx context.Context, q Execer, d *core.CaseDef) error
	UpdateCaseDef       func(ctx context.Context, q Execer, d *core.CaseDef) error
	CreateCaseDefMeta   func(ctx context.Context, q Execer, m core.CaseDefMeta) error
	UpdateCaseDefMeta   func(ctx context.Context, q Execer, m core.CaseDefMeta) error
	CreateExperimentDef func(ctx context.Context, q Execer, d *core.ExperimentDef) error
	// CreateExperimentDefEval links an eval to an experiment, at a position (from 0) in the order the evals run.
	CreateExperimentDefEval func(ctx context.Context, q Execer, experimentDefID core.ExperimentDefID, evalDefID core.EvalDefID, position int) error
	GetExperimentDefByName  func(ctx context.Context, q Execer, name string) (*core.ExperimentDef, error)

	// Runs.
	CreateExperimentRun func(ctx context.Context, q Execer, r *core.ExperimentRun) error
	UpdateExperimentRun func(ctx context.Context, q Execer, r *core.ExperimentRun) error
	CreateEvalRun       func(ctx context.Context, q Execer, r *core.EvalRun) error
	UpdateEvalRun       func(ctx context.Context, q Execer, r *core.EvalRun) error
	CreateEvalRunMeta   func(ctx context.Context, q Execer, m core.EvalRunMeta) error
	UpdateEvalRunMeta   func(ctx context.Context, q Execer, m core.EvalRunMeta) error
	CreateCaseRun       func(ctx context.Context, q Execer, r *core.CaseRun) error
	UpdateCaseRun       func(ctx context.Context, q Execer, r *core.CaseRun) error
	// CreateCaseRunMeta stores the metadata of a case run (a knowledge one with its Trajectories).
	CreateCaseRunMeta func(ctx context.Context, q Execer, m core.CaseRunMeta) error
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

// Unmarshal reads the text of a JSON column into v; an empty text leaves v as it is.
func Unmarshal(text []byte, v any) error {
	if len(text) == 0 {
		return nil
	}
	if err := json.Unmarshal(text, v); err != nil {
		return fmt.Errorf("unmarshal json: %w", err)
	}
	return nil
}

// Affected returns ErrNotFound (wrapped with what) if res says that no row was changed.
func Affected(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}
