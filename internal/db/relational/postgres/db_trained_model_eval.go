package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Evals of trained models: the 'training' rows of the universal eval tables (gb_experiment_runs and gb_experiment_results)
// with their metadata (gb_training_eval_meta) and items (gb_training_eval_items), see db_schema.go and sqlite/db_schema.go.
//
// An eval (model.TrainedModelEval) is ONE RUN with ONE EVAL in it: its ID is that of the gb_experiment_results row, its
// status, error, times and judge cost are the columns of that row, its Params are those of the run, and the rest is in the
// metadata row. A run is in no experiment until StartTrainingExperimentRun links it to one.

// InsertTrainedModelEval stores a new eval (status running unless e.Status says otherwise) and returns its ID, which is the ID of
// its gb_experiment_results row. The ID, the creation time and the finish time are ignored; the counters and the judge's
// usage start as given (normally 0).
func InsertTrainedModelEval(ctx context.Context, d *sql.DB, e model.TrainedModelEval) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("insert trained model eval: %w", err)
	}
	defer tx.Rollback()
	now := Now()
	var runID, id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO gb_experiment_runs (type, status, params, started_at) VALUES ($1, $2, $3::jsonb, $4) RETURNING id`,
		model.EvalTypeTraining, e.Status, e.ParamsJSON(), now).Scan(&runID); err != nil {
		return 0, fmt.Errorf("insert trained model eval: %w", err)
	}
	// The suite is '' and the eval's name the dataset's until training suites exist.
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO gb_experiment_results (type, run_id, suite, eval_name, status, started_at) VALUES ($1, $2, '', $3, $4, $5) RETURNING id`,
		model.EvalTypeTraining, runID, e.Dataset, e.Status, now).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert trained model eval: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gb_training_eval_meta (result_id, trained_model_id, dataset, dataset_sha256, judge_model, num_examples)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, e.TrainedModelID, e.Dataset, e.DatasetSHA256, e.JudgeModel, e.NumExamples); err != nil {
		return 0, fmt.Errorf("insert trained model eval: %w", err)
	}
	if err := tx.Commit(); err != nil {
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
		UPDATE gb_training_eval_meta SET judge_model = $1, num_answered = $2, num_judged = $3, num_failed = $4, num_correct = $5,
			num_partial = $6, num_incorrect = $7, score_sum = $8, num_batches = $9, judge_calls = $10, judge_input_tokens = $11,
			judge_output_tokens = $12 WHERE result_id = $13`,
		e.JudgeModel, e.NumAnswered, e.NumJudged, e.NumFailed, e.NumCorrect, e.NumPartial, e.NumIncorrect, e.ScoreSum,
		e.NumBatches, e.JudgeCalls, e.JudgeInputTokens, e.JudgeOutputTokens, e.ID)
	if err != nil {
		return fmt.Errorf("save eval batch: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gb_experiment_results SET cost_usd = $1 WHERE id = $2`, e.JudgeCostUSD, e.ID); err != nil {
		return fmt.Errorf("save eval batch: %w", err)
	}
	for _, it := range items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_training_eval_items (eval_id, idx, batch, instruction, input, reference, response, status, error,
				score, reason, input_tokens, output_tokens, duration_ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			e.ID, it.Idx, it.Batch, it.Instruction, it.Input, it.Reference, it.Response, it.Status, it.Error,
			it.Score, it.Reason, it.InputTokens, it.OutputTokens, it.DurationMS); err != nil {
			return fmt.Errorf("save eval item %d: %w", it.Idx, err)
		}
	}
	return tx.Commit()
}

// finishTrainingRuns ends the running training runs that have no running eval left (the run of an eval that was just finished),
// setting their totals from their eval and their status to the eval's.
func finishTrainingRuns(ctx context.Context, q rower, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		UPDATE gb_experiment_runs
		SET status       = (SELECT CASE WHEN e.status = 'error' THEN 'failed' ELSE e.status END FROM gb_experiment_results e
				WHERE e.run_id = gb_experiment_runs.id ORDER BY e.id LIMIT 1),
			total_evals  = (SELECT COUNT(*) FROM gb_experiment_results e WHERE e.run_id = gb_experiment_runs.id),
			passed_evals = (SELECT COUNT(*) FROM gb_experiment_results e WHERE e.run_id = gb_experiment_runs.id AND e.status IN ('passed', 'completed')),
			failed_evals = (SELECT COUNT(*) FROM gb_experiment_results e WHERE e.run_id = gb_experiment_runs.id AND e.status NOT IN ('passed', 'completed')),
			cost_usd     = (SELECT COALESCE(SUM(e.cost_usd), 0) FROM gb_experiment_results e WHERE e.run_id = gb_experiment_runs.id),
			finished_at  = $1
		WHERE type = 'training' AND status = 'running'
			AND NOT EXISTS (SELECT 1 FROM gb_experiment_results e WHERE e.run_id = gb_experiment_runs.id AND e.status = 'running')`, now)
	return err
}

// FinishTrainedModelEval ends a running eval with the status and error given, and with it its run. An eval that is not running is
// left as it is (it was finished already); model.ErrNotFound if there is no such eval.
func FinishTrainedModelEval(ctx context.Context, d *sql.DB, id int64, status, errText string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("finish trained model eval: %w", err)
	}
	defer tx.Rollback()
	now := Now()
	res, err := tx.ExecContext(ctx, `
		UPDATE gb_experiment_results SET status = $1, error = $2, finished_at = $3
		WHERE id = $4 AND type = 'training' AND status = 'running'`, status, errText, now, id)
	if err != nil {
		return fmt.Errorf("finish trained model eval: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM gb_experiment_results WHERE id = $1 AND type = 'training'`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return model.ErrNotFound
		}
		return nil
	}
	if err := finishTrainingRuns(ctx, tx, now); err != nil {
		return fmt.Errorf("finish trained model eval: %w", err)
	}
	return tx.Commit()
}

// FailRunningTrainedModelEvals sets every running eval to failed with the reason (and ends its run), and returns how many there were.
func FailRunningTrainedModelEvals(ctx context.Context, d *sql.DB, reason string) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("fail running trained model evals: %w", err)
	}
	defer tx.Rollback()
	now := Now()
	res, err := tx.ExecContext(ctx, `
		UPDATE gb_experiment_results SET status = 'failed', error = $1, finished_at = $2
		WHERE type = 'training' AND status = 'running'`, reason, now)
	if err != nil {
		return 0, fmt.Errorf("fail running trained model evals: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := finishTrainingRuns(ctx, tx, now); err != nil {
		return 0, fmt.Errorf("fail running trained model evals: %w", err)
	}
	return n, tx.Commit()
}

// trainedModelEvalColumns and trainedModelEvalFrom select a model.TrainedModelEval (scanTrainedModelEval): the result e, its
// training metadata tm and its run r (which holds the params).
const trainedModelEvalColumns = `e.id, tm.trained_model_id, e.status, tm.dataset, tm.dataset_sha256, r.params::text, tm.judge_model,
	tm.num_examples, tm.num_answered, tm.num_judged, tm.num_failed, tm.num_correct, tm.num_partial, tm.num_incorrect, tm.score_sum,
	tm.num_batches, tm.judge_calls, tm.judge_input_tokens, tm.judge_output_tokens, e.cost_usd, e.error, e.started_at, e.finished_at`

const trainedModelEvalFrom = ` FROM gb_experiment_results e
	JOIN gb_training_eval_meta tm ON tm.result_id = e.id
	JOIN gb_experiment_runs r ON r.id = e.run_id`

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
	e, err := scanTrainedModelEval(d.QueryRowContext(ctx, `SELECT `+trainedModelEvalColumns+trainedModelEvalFrom+` WHERE e.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return e, err
}

// ListTrainedModelEvals returns the newest evals first (of one trained model, or of all for modelID 0), at most limit (all for limit <= 0).
func ListTrainedModelEvals(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]model.TrainedModelEval, error) {
	q := `SELECT ` + trainedModelEvalColumns + trainedModelEvalFrom
	var args []any
	if modelID > 0 {
		args = append(args, modelID)
		q += fmt.Sprintf(` WHERE tm.trained_model_id = $%d`, len(args))
	}
	q += ` ORDER BY e.id DESC`
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
			output_tokens, duration_ms FROM gb_training_eval_items WHERE eval_id = $1 ORDER BY idx`, evalID)
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
