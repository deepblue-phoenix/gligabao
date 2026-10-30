package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
)

// Store holds the functions of this package, for code that works with either backend (db.Store).
var Store = db.Store{
	CreateRun:   CreateRun,
	EnterState:  EnterState,
	CloseState:  CloseState,
	UpdateUsage: UpdateUsage,
	FinishRun:   FinishRun,
	FailActive:  FailActive,
	GetRun:      GetRun,
	ListRuns:    ListRuns,
	ListStates:  ListStates,
}

// now is the current time in the precision of the storage: UTC, milliseconds (a state lasts from a few milliseconds to a minute;
// whole seconds would hide the steps), the same as the sqlite backend.
func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// norm normalizes t for storage.
func norm(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }

// ptr normalizes t for storage; nil is NULL.
func ptr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return norm(*t)
}

// timePtr reads a nullable time.
func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

// scanner is what *sql.Row and *sql.Rows share.
type scanner interface{ Scan(dest ...any) error }

// CreateRun inserts r into gb_agent_research_runs and sets its ID, CreatedAt, and Status (core.StatusRunning if empty). It adds no
// state: see EnterState.
func CreateRun(ctx context.Context, q db.Execer, r *core.Run) error {
	if r.Status == "" {
		r.Status = core.StatusRunning
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now()
	}
	r.CreatedAt = norm(r.CreatedAt)
	var id int64
	if err := q.QueryRowContext(ctx, `
		INSERT INTO gb_agent_research_runs (user_id, parent_id, name, topic, status, state, model, max_steps, steps, tool_calls, input_tokens,
			output_tokens, cost_usd, report, error, created_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING id`,
		r.UserID, r.ParentID, r.Name, r.Topic, string(r.Status), r.State, r.Model, r.MaxSteps, r.Steps, r.ToolCalls, r.InputTokens, r.OutputTokens,
		r.CostUSD, r.Report, r.Error, r.CreatedAt, ptr(r.FinishedAt)).Scan(&id); err != nil {
		return fmt.Errorf("create agent research run: %w", err)
	}
	r.ID = id
	return nil
}

// EnterState records that the run runID moves to state: it ends the state the run is in (the one without a finished_at), adds the
// new state to gb_agent_research_states, numbered after the last, and sets the run's state. It returns the new state. Run it in a
// transaction to make the three writes one: with a *sql.DB they are three statements.
func EnterState(ctx context.Context, q db.Execer, runID int64, state string, step int, detail string) (*core.State, error) {
	at := now()
	if _, err := q.ExecContext(ctx, `UPDATE gb_agent_research_states SET finished_at = $1 WHERE run_id = $2 AND finished_at IS NULL`,
		at, runID); err != nil {
		return nil, fmt.Errorf("enter state %q of agent research run %d: %w", state, runID, err)
	}
	s := &core.State{RunID: runID, State: state, Step: step, Detail: detail, StartedAt: at}
	if err := q.QueryRowContext(ctx, `
		INSERT INTO gb_agent_research_states (run_id, seq, state, step, detail, started_at)
		VALUES ($1, (SELECT COALESCE(MAX(seq), 0) + 1 FROM gb_agent_research_states WHERE run_id = $1), $2, $3, $4, $5)
		RETURNING id, seq`, runID, state, step, detail, at).Scan(&s.ID, &s.Seq); err != nil {
		return nil, fmt.Errorf("enter state %q of agent research run %d: %w", state, runID, err)
	}
	if _, err := q.ExecContext(ctx, `UPDATE gb_agent_research_runs SET state = $1 WHERE id = $2`, state, runID); err != nil {
		return nil, fmt.Errorf("enter state %q of agent research run %d: %w", state, runID, err)
	}
	return s, nil
}

// CloseState ends the state stateID (sets its finished_at, if it has none) and its error ("" if it did not fail). It does not
// change the run's state: the next EnterState does.
func CloseState(ctx context.Context, q db.Execer, stateID int64, errText string) error {
	res, err := q.ExecContext(ctx, `UPDATE gb_agent_research_states SET finished_at = $1, error = $2 WHERE id = $3 AND finished_at IS NULL`,
		now(), errText, stateID)
	if err != nil {
		return fmt.Errorf("close agent research state %d: %w", stateID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := q.QueryRowContext(ctx, `SELECT 1 FROM gb_agent_research_states WHERE id = $1`, stateID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return db.ErrNotFound
		}
	}
	return nil
}

// UpdateUsage writes the counters of a run that is going (see db.Usage).
func UpdateUsage(ctx context.Context, q db.Execer, runID int64, u db.Usage) error {
	res, err := q.ExecContext(ctx, `
		UPDATE gb_agent_research_runs SET steps = $1, tool_calls = $2, input_tokens = $3, output_tokens = $4, cost_usd = $5 WHERE id = $6`,
		u.Steps, u.ToolCalls, u.InputTokens, u.OutputTokens, u.CostUSD, runID)
	if err != nil {
		return fmt.Errorf("update usage of agent research run %d: %w", runID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// FinishRun ends a running run: its status (completed or failed), report and error, and its finished_at (now); it ends the state the
// run is in (with the error, if it has none of its own) and adds the terminal state (completed or failed, with the error). It
// returns false, and writes nothing, if the run was finished already; db.ErrNotFound if there is no such run. Run it in a
// transaction to make the writes one.
func FinishRun(ctx context.Context, q db.Execer, id int64, status core.Status, report, errText string) (bool, error) {
	if status.Active() || status == "" {
		return false, fmt.Errorf("finish agent research run %d: status %q is not a final one", id, status)
	}
	at := now()
	res, err := q.ExecContext(ctx, `
		UPDATE gb_agent_research_runs SET status = $1, state = $2, report = $3, error = $4, finished_at = $5
		WHERE id = $6 AND status = 'running'`, string(status), string(status), report, errText, at, id)
	if err != nil {
		return false, fmt.Errorf("finish agent research run %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := q.QueryRowContext(ctx, `SELECT 1 FROM gb_agent_research_runs WHERE id = $1`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return false, db.ErrNotFound
		}
		return false, nil
	}
	if _, err := q.ExecContext(ctx, `
		UPDATE gb_agent_research_states SET finished_at = $1, error = CASE WHEN error = '' THEN $2::text ELSE error END
		WHERE run_id = $3 AND finished_at IS NULL`, at, errText, id); err != nil {
		return false, fmt.Errorf("finish agent research run %d: %w", id, err)
	}
	if _, err := q.ExecContext(ctx, `
		INSERT INTO gb_agent_research_states (run_id, seq, state, step, detail, error, started_at, finished_at)
		VALUES ($1, (SELECT COALESCE(MAX(seq), 0) + 1 FROM gb_agent_research_states WHERE run_id = $1), $2, 0, '', $3, $4, $4)`,
		id, string(status), errText, at); err != nil {
		return false, fmt.Errorf("finish agent research run %d: %w", id, err)
	}
	return true, nil
}

// FailActive ends every running run as failed, with the reason as its error (the ones a restart of the server interrupted), and
// returns how many there were.
func FailActive(ctx context.Context, q db.Execer, reason string) (int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM gb_agent_research_runs WHERE status = 'running' ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("fail active agent research runs: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("fail active agent research runs: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("fail active agent research runs: %w", err)
	}
	rows.Close()
	var n int64
	for _, id := range ids {
		ok, err := FinishRun(ctx, q, id, core.StatusFailed, "", reason)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

const runColumns = `id, user_id, parent_id, name, topic, status, state, model, max_steps, steps, tool_calls, input_tokens, output_tokens, cost_usd,
	report, error, created_at, finished_at`

func scanRun(s scanner) (*core.Run, error) {
	var r core.Run
	var status string
	var created time.Time
	var finished sql.NullTime
	var parent sql.NullInt64
	if err := s.Scan(&r.ID, &r.UserID, &parent, &r.Name, &r.Topic, &status, &r.State, &r.Model, &r.MaxSteps, &r.Steps, &r.ToolCalls, &r.InputTokens,
		&r.OutputTokens, &r.CostUSD, &r.Report, &r.Error, &created, &finished); err != nil {
		return nil, err
	}
	r.Status = core.Status(status)
	if parent.Valid {
		r.ParentID = &parent.Int64
	}
	r.CreatedAt, r.FinishedAt = created.UTC(), timePtr(finished)
	return &r, nil
}

// GetRun returns the run with that ID, without its States (see ListStates), or db.ErrNotFound.
func GetRun(ctx context.Context, q db.Execer, id int64) (*core.Run, error) {
	r, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM gb_agent_research_runs WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get agent research run %d: %w", id, err)
	}
	return r, nil
}

// ListRuns returns the runs that match f, newest first, at most limit of them (all for limit <= 0), without their States.
func ListRuns(ctx context.Context, q db.Execer, f core.RunFilter, limit int) ([]core.Run, error) {
	query := `SELECT ` + runColumns + ` FROM gb_agent_research_runs`
	var where []string
	var args []any
	if f.UserID != "" {
		args = append(args, f.UserID)
		where = append(where, fmt.Sprintf(`user_id = $%d`, len(args)))
	}
	if f.ParentID != nil {
		args = append(args, *f.ParentID)
		where = append(where, fmt.Sprintf(`parent_id = $%d`, len(args)))
	}
	if len(f.Statuses) > 0 {
		marks := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			args = append(args, string(st))
			marks[i] = fmt.Sprintf("$%d", len(args))
		}
		where = append(where, `status IN (`+strings.Join(marks, ", ")+`)`)
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY id DESC`
	if limit > 0 {
		args = append(args, limit)
		query += fmt.Sprintf(` LIMIT $%d`, len(args))
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list agent research runs: %w", err)
	}
	defer rows.Close()
	out := []core.Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list agent research runs: %w", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agent research runs: %w", err)
	}
	return out, nil
}

// ListStates returns the states of the run, in the order they were entered. It is empty, not an error, for a run without states or
// one that does not exist.
func ListStates(ctx context.Context, q db.Execer, runID int64) ([]core.State, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, run_id, seq, state, step, detail, error, started_at, finished_at
		FROM gb_agent_research_states WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("list states of agent research run %d: %w", runID, err)
	}
	defer rows.Close()
	out := []core.State{}
	for rows.Next() {
		var s core.State
		var started time.Time
		var finished sql.NullTime
		if err := rows.Scan(&s.ID, &s.RunID, &s.Seq, &s.State, &s.Step, &s.Detail, &s.Error, &started, &finished); err != nil {
			return nil, fmt.Errorf("list states of agent research run %d: %w", runID, err)
		}
		s.StartedAt, s.FinishedAt = started.UTC(), timePtr(finished)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list states of agent research run %d: %w", runID, err)
	}
	return out, nil
}
