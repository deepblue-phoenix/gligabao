package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Training data audits of trained models (model.TrainedModelRun): gb_training_audit_runs, one row per audit, and
// gb_training_audit_cases, one row per example, see db_schema.go. Separate from the evals of trained models (db_trained_model_eval.go).

// InsertTrainedModelAuditRun stores a new audit run (status running unless a.Status says otherwise) and returns its ID. The ID, the
// creation time and the finish time are ignored; the counters and the judge's usage start as given (normally 0).
func InsertTrainedModelAuditRun(ctx context.Context, d *sql.DB, a model.TrainedModelRun) (int64, error) {
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_training_audit_runs (trained_model_id, status, dataset, dataset_sha256, params, judge_model, num_examples, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.TrainedModelID, a.Status, a.Dataset, a.DatasetSHA256, a.ParamsJSON(), a.JudgeModel, a.NumExamples, FormatTime(Now()))
	if err != nil {
		return 0, fmt.Errorf("insert trained model audit run: %w", err)
	}
	return res.LastInsertId()
}

// SaveTrainedModelAuditRunBatch stores the cases of a finished batch and the audit run's counters after it (a.ID names the audit
// run; its judge model, counters, the judge's usage and cost are written), in one transaction. model.ErrNotFound if there is no
// such audit run.
func SaveTrainedModelAuditRunBatch(ctx context.Context, d *sql.DB, a model.TrainedModelRun, cases []model.TrainedModelCase) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save audit run batch: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE gb_training_audit_runs SET judge_model = ?, num_answered = ?, num_judged = ?, num_errored = ?, num_correct = ?,
			num_partial = ?, num_incorrect = ?, score_sum = ?, num_batches = ?, judge_calls = ?, judge_input_tokens = ?,
			judge_output_tokens = ?, judge_cost_usd = ? WHERE id = ?`,
		a.JudgeModel, a.NumAnswered, a.NumJudged, a.NumErrored, a.NumCorrect, a.NumPartial, a.NumIncorrect, a.ScoreSum,
		a.NumBatches, a.JudgeCalls, a.JudgeInputTokens, a.JudgeOutputTokens, a.JudgeCostUSD, a.ID)
	if err != nil {
		return fmt.Errorf("save audit run batch: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	for _, it := range cases {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_training_audit_cases (run_id, idx, batch, instruction, input, reference, response, status, error, score, reason,
				input_tokens, output_tokens, duration_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, it.Idx, it.Batch, it.Instruction, it.Input, it.Reference, it.Response, it.Status, it.Error, it.Score, it.Reason,
			it.InputTokens, it.OutputTokens, it.DurationMS); err != nil {
			return fmt.Errorf("save audit run case %d: %w", it.Idx, err)
		}
	}
	return tx.Commit()
}

// finishAuditRuns ends the running audit run with that ID (every running one for id 0) with the status and error, and returns how
// many it ended.
func finishAuditRuns(ctx context.Context, q rower, id int64, status, errText, now string) (int64, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE gb_training_audit_runs SET status = ?, error = ?, finished_at = ?
		WHERE status = 'running' AND (? = 0 OR id = ?)`, status, errText, now, id, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FinishTrainedModelAuditRun ends a running audit run with the status and error given. An audit run that is not running is left as
// it is (it was finished already); model.ErrNotFound if there is no such audit run.
func FinishTrainedModelAuditRun(ctx context.Context, d *sql.DB, id int64, status, errText string) error {
	n, err := finishAuditRuns(ctx, d, id, status, errText, FormatTime(Now()))
	if err != nil {
		return fmt.Errorf("finish trained model audit run: %w", err)
	}
	if n == 0 {
		var one int
		if err := d.QueryRowContext(ctx, `SELECT 1 FROM gb_training_audit_runs WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return model.ErrNotFound
		}
	}
	return nil
}

// FailRunningTrainedModelAuditRuns sets every running audit run to error with the reason, and returns how many there were.
func FailRunningTrainedModelAuditRuns(ctx context.Context, d *sql.DB, reason string) (int64, error) {
	n, err := finishAuditRuns(ctx, d, 0, model.TrainedModelRunErrored, reason, FormatTime(Now()))
	if err != nil {
		return 0, fmt.Errorf("fail running trained model audit runs: %w", err)
	}
	return n, nil
}

const trainedModelAuditRunColumns = `id, trained_model_id, status, dataset, dataset_sha256, params, judge_model, num_examples, num_answered,
	num_judged, num_errored, num_correct, num_partial, num_incorrect, score_sum, num_batches, judge_calls, judge_input_tokens,
	judge_output_tokens, judge_cost_usd, error, started_at, finished_at`

func scanTrainedModelAuditRun(s rowScanner) (*model.TrainedModelRun, error) {
	a := model.TrainedModelRun{Kind: model.TrainedModelKindAudit}
	var params, created string
	var finished sql.NullString
	if err := s.Scan(&a.ID, &a.TrainedModelID, &a.Status, &a.Dataset, &a.DatasetSHA256, &params, &a.JudgeModel, &a.NumExamples,
		&a.NumAnswered, &a.NumJudged, &a.NumErrored, &a.NumCorrect, &a.NumPartial, &a.NumIncorrect, &a.ScoreSum, &a.NumBatches,
		&a.JudgeCalls, &a.JudgeInputTokens, &a.JudgeOutputTokens, &a.JudgeCostUSD, &a.Error, &created, &finished); err != nil {
		return nil, err
	}
	a.SetStoredParams(params)
	a.CreatedAt = parseStoredTime(created)
	if finished.Valid {
		a.FinishedAt = ParseTime(finished.String)
	}
	return &a, nil
}

// GetTrainedModelAuditRun returns the audit run with that ID, or model.ErrNotFound.
func GetTrainedModelAuditRun(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModelRun, error) {
	a, err := scanTrainedModelAuditRun(d.QueryRowContext(ctx, `SELECT `+trainedModelAuditRunColumns+` FROM gb_training_audit_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return a, err
}

// ListTrainedModelAuditRuns returns the newest audit runs first (of one trained model, or of all for modelID 0), at most limit (all for limit <= 0).
func ListTrainedModelAuditRuns(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]model.TrainedModelRun, error) {
	q := `SELECT ` + trainedModelAuditRunColumns + ` FROM gb_training_audit_runs`
	var args []any
	if modelID > 0 {
		q += ` WHERE trained_model_id = ?`
		args = append(args, modelID)
	}
	q += ` ORDER BY id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list trained model audit runs: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelRun{}
	for rows.Next() {
		a, err := scanTrainedModelAuditRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ListTrainedModelAuditCases returns the cases of an audit run stored so far, in dataset order.
func ListTrainedModelAuditCases(ctx context.Context, d *sql.DB, auditRunID int64) ([]model.TrainedModelCase, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, run_id, idx, batch, instruction, input, reference, response, status, error, score, reason, input_tokens,
			output_tokens, duration_ms
		FROM gb_training_audit_cases WHERE run_id = ? ORDER BY idx`, auditRunID)
	if err != nil {
		return nil, fmt.Errorf("list trained model audit run cases: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelCase{}
	for rows.Next() {
		var it model.TrainedModelCase
		var score sql.NullInt64
		if err := rows.Scan(&it.ID, &it.RunID, &it.Idx, &it.Batch, &it.Instruction, &it.Input, &it.Reference, &it.Response,
			&it.Status, &it.Error, &score, &it.Reason, &it.InputTokens, &it.OutputTokens, &it.DurationMS); err != nil {
			return nil, err
		}
		if score.Valid {
			s := int(score.Int64)
			it.Score = &s
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
