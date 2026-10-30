// Package db is the storage interface of the research agents (package agent_research): the Store, the functions of a backend, which
// are in the subpackages sqlite and postgres. They all take an Execer, so they can run in a transaction.
package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
)

// ErrNotFound means the run or the state asked for does not exist.
var ErrNotFound = errors.New("agent research: not found")

// Execer is what *sql.DB and *sql.Tx share for running statements.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store is the functions of a backend as a value, so that the code that runs the agents can be given either backend (sqlite.Store or
// postgres.Store) without knowing which. The functions are documented in the backends.
type Store struct {
	CreateRun   func(ctx context.Context, q Execer, r *core.Run) error
	EnterState  func(ctx context.Context, q Execer, runID int64, state string, step int, detail string) (*core.State, error)
	CloseState  func(ctx context.Context, q Execer, stateID int64, errText string) error
	UpdateUsage func(ctx context.Context, q Execer, runID int64, u Usage) error
	FinishRun   func(ctx context.Context, q Execer, id int64, status core.Status, report, errText string) (bool, error)
	FailActive  func(ctx context.Context, q Execer, reason string) (int64, error)
	GetRun      func(ctx context.Context, q Execer, id int64) (*core.Run, error)
	ListRuns    func(ctx context.Context, q Execer, f core.RunFilter, limit int) ([]core.Run, error)
	ListStates  func(ctx context.Context, q Execer, runID int64) ([]core.State, error)
}

// Usage is what a run has spent so far: the progress counters of a run.
type Usage struct {
	Steps, ToolCalls          int
	InputTokens, OutputTokens int
	CostUSD                   float64
}
