package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db"
)

// Store holds the functions of this package, for code that works with either backend (db.Store).
var Store = db.Store{
	CreateAuditRun:       CreateAuditRun,
	CreateAuditCaseRun:   CreateAuditCaseRun,
	UpdateAuditRun:       UpdateAuditRun,
	FinishAuditRun:       FinishAuditRun,
	FailRunningAuditRuns: FailRunningAuditRuns,
	GetAuditRun:          GetAuditRun,
	ListAuditRuns:        ListAuditRuns,
	ListAuditCaseRuns:    ListAuditCaseRuns,
}

// parseTime reads a time written by text.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// parseTimePtr reads a nullable time written by textPtr.
func parseTimePtr(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// scanner is what *sql.Row and *sql.Rows share.
type scanner interface{ Scan(dest ...any) error }

const runColumns = `id, trained_model_id, dataset, dataset_sha256, status, error, params,
	counts_total, counts_pending, counts_running, counts_completed, counts_errored, counts_cancelled, cost_usd,
	judge_model, num_answered, num_judged, num_correct, num_partial, num_incorrect, score_sum, num_batches, judge_calls,
	judge_input_tokens, judge_output_tokens, created_at, started_at, finished_at`

func scanRun(s scanner) (*core.AuditRun, error) {
	var r core.AuditRun
	var id int64
	var status, params, created string
	var started, finished sql.NullString
	if err := s.Scan(&id, &r.TrainedModelID, &r.Dataset, &r.DatasetSHA256, &status, &r.Error, &params,
		&r.Counts.Total, &r.Counts.Pending, &r.Counts.Running, &r.Counts.Completed, &r.Counts.Errored, &r.Counts.Cancelled, &r.CostUSD,
		&r.JudgeModel, &r.NumAnswered, &r.NumJudged, &r.NumCorrect, &r.NumPartial, &r.NumIncorrect, &r.ScoreSum, &r.NumBatches,
		&r.JudgeCalls, &r.JudgeInputTokens, &r.JudgeOutputTokens, &created, &started, &finished); err != nil {
		return nil, err
	}
	r.ID, r.Status = core.AuditRunID(id), core.Status(status)
	var err error
	if r.Params, err = db.ParseParams([]byte(params)); err != nil {
		return nil, err
	}
	if r.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if r.StartedAt, err = parseTimePtr(started); err != nil {
		return nil, err
	}
	if r.FinishedAt, err = parseTimePtr(finished); err != nil {
		return nil, err
	}
	return &r, nil
}

// GetAuditRun returns the audit run with that ID, without its CaseRuns (see ListAuditCaseRuns), or db.ErrNotFound.
func GetAuditRun(ctx context.Context, q db.Execer, id core.AuditRunID) (*core.AuditRun, error) {
	r, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM gb_neo_training_audit_runs WHERE id = ?`, int64(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get training audit run %d: %w", id, err)
	}
	return r, nil
}

// ListAuditRuns returns the audit runs of trained model trainedModelID (of every model for 0), newest first, at most limit of them
// (all for limit <= 0), without their CaseRuns.
func ListAuditRuns(ctx context.Context, q db.Execer, trainedModelID int64, limit int) ([]core.AuditRun, error) {
	query := `SELECT ` + runColumns + ` FROM gb_neo_training_audit_runs`
	var args []any
	if trainedModelID != 0 {
		query += ` WHERE trained_model_id = ?`
		args = append(args, trainedModelID)
	}
	query += ` ORDER BY id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list training audit runs: %w", err)
	}
	defer rows.Close()
	out := []core.AuditRun{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list training audit runs: %w", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list training audit runs: %w", err)
	}
	return out, nil
}

// ListAuditCaseRuns returns the case runs of audit run runID written so far, in dataset order (by Idx). It is empty, not an
// error, for a run without case runs or one that does not exist.
func ListAuditCaseRuns(ctx context.Context, q db.Execer, runID core.AuditRunID) ([]core.AuditCaseRun, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, run_id, idx, status, error, instruction, input, reference, response, batch, judge_score, judge_reason,
			input_tokens, output_tokens, duration_ms, created_at
		FROM gb_neo_training_audit_case_runs WHERE run_id = ? ORDER BY idx`, int64(runID))
	if err != nil {
		return nil, fmt.Errorf("list case runs of training audit run %d: %w", runID, err)
	}
	defer rows.Close()
	out := []core.AuditCaseRun{}
	for rows.Next() {
		var c core.AuditCaseRun
		var id, run int64
		var status, created string
		var score sql.NullInt64
		if err := rows.Scan(&id, &run, &c.Idx, &status, &c.Error, &c.Instruction, &c.Input, &c.Reference, &c.Response, &c.Batch,
			&score, &c.JudgeReason, &c.InputTokens, &c.OutputTokens, &c.DurationMS, &created); err != nil {
			return nil, fmt.Errorf("list case runs of training audit run %d: %w", runID, err)
		}
		c.ID, c.RunID, c.Status = core.AuditCaseRunID(id), core.AuditRunID(run), core.Status(status)
		if score.Valid {
			s := int(score.Int64)
			c.JudgeScore = &s
		}
		if c.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("list case runs of training audit run %d: %w", runID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list case runs of training audit run %d: %w", runID, err)
	}
	return out, nil
}

// UpdateAuditRun writes what changes while a run goes: its Status, Error, Counts, CostUSD, AuditStats and its StartedAt and
// FinishedAt. What the run was made with (the trained model, the dataset, Params, CreatedAt) is not touched. db.ErrNotFound if
// there is no run r.ID. The progress of a running audit is written with it after every judge batch, in the transaction that
// creates the batch's case runs.
func UpdateAuditRun(ctx context.Context, q db.Execer, r *core.AuditRun) error {
	res, err := q.ExecContext(ctx, `
		UPDATE gb_neo_training_audit_runs SET status = ?, error = ?, counts_total = ?, counts_pending = ?, counts_running = ?,
			counts_completed = ?, counts_errored = ?, counts_cancelled = ?, cost_usd = ?, judge_model = ?, num_answered = ?,
			num_judged = ?, num_correct = ?, num_partial = ?, num_incorrect = ?, score_sum = ?, num_batches = ?, judge_calls = ?,
			judge_input_tokens = ?, judge_output_tokens = ?, started_at = ?, finished_at = ?
		WHERE id = ?`,
		string(status(r.Status)), r.Error, r.Counts.Total, r.Counts.Pending, r.Counts.Running, r.Counts.Completed, r.Counts.Errored,
		r.Counts.Cancelled, r.CostUSD, r.JudgeModel, r.NumAnswered, r.NumJudged, r.NumCorrect, r.NumPartial, r.NumIncorrect,
		r.ScoreSum, r.NumBatches, r.JudgeCalls, r.JudgeInputTokens, r.JudgeOutputTokens, textPtr(r.StartedAt), textPtr(r.FinishedAt),
		int64(r.ID))
	if err != nil {
		return fmt.Errorf("update training audit run %d: %w", r.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// FinishAuditRun ends a run that is pending or running: it sets its final status (completed, errored or cancelled), its error
// text (for errored) and FinishedAt (now). A run that is finished already is left as it is. db.ErrNotFound if there is no such run.
func FinishAuditRun(ctx context.Context, q db.Execer, id core.AuditRunID, st core.Status, errText string) error {
	if !st.Finished() {
		return fmt.Errorf("finish training audit run %d: status %q is not a final one", id, st)
	}
	n, err := finish(ctx, q, int64(id), st, errText)
	if err != nil {
		return fmt.Errorf("finish training audit run %d: %w", id, err)
	}
	if n == 0 {
		var one int
		if err := q.QueryRowContext(ctx, `SELECT 1 FROM gb_neo_training_audit_runs WHERE id = ?`, int64(id)).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return db.ErrNotFound
		}
	}
	return nil
}

// FailRunningAuditRuns errors every run that is pending or running, with the reason as its error (the ones a restart of the server
// interrupted), and returns how many there were.
func FailRunningAuditRuns(ctx context.Context, q db.Execer, reason string) (int64, error) {
	n, err := finish(ctx, q, 0, core.StatusErrored, reason)
	if err != nil {
		return 0, fmt.Errorf("fail running training audit runs: %w", err)
	}
	return n, nil
}

// finish ends the unfinished run id (every unfinished run for id 0) and returns how many it ended.
func finish(ctx context.Context, q db.Execer, id int64, st core.Status, errText string) (int64, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE gb_neo_training_audit_runs SET status = ?, error = ?, finished_at = ?
		WHERE status IN ('pending', 'running') AND (? = 0 OR id = ?)`, string(st), errText, text(now()), id, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
