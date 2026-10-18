package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Evals of trained models (gb_trained_model_evals and gb_trained_model_eval_items, see db_schema.go)

// InsertTrainedModelEval stores a new eval (status running unless e.Status says otherwise) and returns its ID. The ID,
// the creation time and the finish time are ignored; the counters and the judge's usage start as given (normally 0).
func InsertTrainedModelEval(ctx context.Context, d *sql.DB, e model.TrainedModelEval) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx, `
		INSERT INTO gb_trained_model_evals (trained_model_id, status, dataset, dataset_sha256, params, judge_model, num_examples, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		e.TrainedModelID, e.Status, e.Dataset, e.DatasetSHA256, e.ParamsJSON(), e.JudgeModel, e.NumExamples, Now()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert trained model eval: %w", err)
	}
	return id, nil
}

// SaveTrainedModelEvalBatch stores the items of a finished batch and the eval's counters after it (e.ID names the eval; its
// judge model, counters and the judge's usage are written), in one transaction. model.ErrNotFound if there is no such eval.
func SaveTrainedModelEvalBatch(ctx context.Context, d *sql.DB, e model.TrainedModelEval, items []model.TrainedModelEvalItem) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save eval batch: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE gb_trained_model_evals SET judge_model = $1, num_answered = $2, num_judged = $3, num_failed = $4, num_correct = $5,
			num_partial = $6, num_incorrect = $7, score_sum = $8, num_batches = $9, judge_calls = $10, judge_input_tokens = $11,
			judge_output_tokens = $12, judge_cost_usd = $13 WHERE id = $14`,
		e.JudgeModel, e.NumAnswered, e.NumJudged, e.NumFailed, e.NumCorrect, e.NumPartial, e.NumIncorrect, e.ScoreSum,
		e.NumBatches, e.JudgeCalls, e.JudgeInputTokens, e.JudgeOutputTokens, e.JudgeCostUSD, e.ID)
	if err != nil {
		return fmt.Errorf("save eval batch: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	for _, it := range items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_trained_model_eval_items (eval_id, idx, batch, instruction, input, reference, response, status, error,
				score, reason, input_tokens, output_tokens, duration_ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			e.ID, it.Idx, it.Batch, it.Instruction, it.Input, it.Reference, it.Response, it.Status, it.Error,
			it.Score, it.Reason, it.InputTokens, it.OutputTokens, it.DurationMS); err != nil {
			return fmt.Errorf("save eval item %d: %w", it.Idx, err)
		}
	}
	return tx.Commit()
}

// FinishTrainedModelEval ends a running eval with the status and error given. An eval that is not running is left as
// it is (it was finished already); model.ErrNotFound if there is no such eval.
func FinishTrainedModelEval(ctx context.Context, d *sql.DB, id int64, status, errText string) error {
	res, err := d.ExecContext(ctx, `
		UPDATE gb_trained_model_evals SET status = $1, error = $2, finished_at = $3 WHERE id = $4 AND status = 'running'`,
		status, errText, Now(), id)
	if err != nil {
		return fmt.Errorf("finish trained model eval: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := d.QueryRowContext(ctx, `SELECT 1 FROM gb_trained_model_evals WHERE id = $1`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return model.ErrNotFound
		}
	}
	return nil
}

// FailRunningTrainedModelEvals sets every running eval to failed with the reason, and returns how many there were.
func FailRunningTrainedModelEvals(ctx context.Context, d *sql.DB, reason string) (int64, error) {
	res, err := d.ExecContext(ctx, `
		UPDATE gb_trained_model_evals SET status = 'failed', error = $1, finished_at = $2 WHERE status = 'running'`,
		reason, Now())
	if err != nil {
		return 0, fmt.Errorf("fail running trained model evals: %w", err)
	}
	return res.RowsAffected()
}

const trainedModelEvalColumns = `id, trained_model_id, status, dataset, dataset_sha256, params, judge_model, num_examples,
	num_answered, num_judged, num_failed, num_correct, num_partial, num_incorrect, score_sum, num_batches, judge_calls,
	judge_input_tokens, judge_output_tokens, judge_cost_usd, error, created_at, finished_at`

func scanTrainedModelEval(s rowScanner) (*model.TrainedModelEval, error) {
	var e model.TrainedModelEval
	var params string
	var finished sql.NullTime
	if err := s.Scan(&e.ID, &e.TrainedModelID, &e.Status, &e.Dataset, &e.DatasetSHA256, &params, &e.JudgeModel, &e.NumExamples,
		&e.NumAnswered, &e.NumJudged, &e.NumFailed, &e.NumCorrect, &e.NumPartial, &e.NumIncorrect, &e.ScoreSum, &e.NumBatches,
		&e.JudgeCalls, &e.JudgeInputTokens, &e.JudgeOutputTokens, &e.JudgeCostUSD, &e.Error, &e.CreatedAt, &finished); err != nil {
		return nil, err
	}
	e.SetStoredParams(params)
	e.CreatedAt = e.CreatedAt.UTC()
	if finished.Valid {
		t := finished.Time.UTC()
		e.FinishedAt = &t
	}
	return &e, nil
}

// GetTrainedModelEval returns the eval with that ID, or model.ErrNotFound.
func GetTrainedModelEval(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModelEval, error) {
	e, err := scanTrainedModelEval(d.QueryRowContext(ctx, `SELECT `+trainedModelEvalColumns+` FROM gb_trained_model_evals WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return e, err
}

// ListTrainedModelEvals returns the newest evals first (of one trained model, or of all for modelID 0), at most limit (all for limit <= 0).
func ListTrainedModelEvals(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]model.TrainedModelEval, error) {
	q := `SELECT ` + trainedModelEvalColumns + ` FROM gb_trained_model_evals`
	var args []any
	if modelID > 0 {
		args = append(args, modelID)
		q += fmt.Sprintf(` WHERE trained_model_id = $%d`, len(args))
	}
	q += ` ORDER BY id DESC`
	if limit > 0 {
		args = append(args, limit)
		q += fmt.Sprintf(` LIMIT $%d`, len(args))
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list trained model evals: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelEval{}
	for rows.Next() {
		e, err := scanTrainedModelEval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ListTrainedModelEvalItems returns the items of an eval stored so far, in dataset order.
func ListTrainedModelEvalItems(ctx context.Context, d *sql.DB, evalID int64) ([]model.TrainedModelEvalItem, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, eval_id, idx, batch, instruction, input, reference, response, status, error, score, reason, input_tokens,
			output_tokens, duration_ms FROM gb_trained_model_eval_items WHERE eval_id = $1 ORDER BY idx`, evalID)
	if err != nil {
		return nil, fmt.Errorf("list trained model eval items: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelEvalItem{}
	for rows.Next() {
		var it model.TrainedModelEvalItem
		var score sql.NullInt64
		if err := rows.Scan(&it.ID, &it.EvalID, &it.Idx, &it.Batch, &it.Instruction, &it.Input, &it.Reference, &it.Response,
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
