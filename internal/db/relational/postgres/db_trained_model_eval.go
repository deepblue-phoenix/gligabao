package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Eval runs of trained models: the 'training' rows of the universal eval tables (gb_experiment_runs and gb_experiment_case_results)
// with their metadata (gb_training_eval_run_meta and gb_training_case_meta), see db_schema.go and sqlite/db_schema.go.
//
// An eval run (model.TrainedModelEvalRun) is ONE RUN: its ID is the run's, its status, times and Params are the columns of the
// gb_experiment_runs row, and the rest is in the run's metadata row. Each example of the eval is a case result of that run (a
// gb_experiment_case_results row with a gb_training_case_meta row). A run is in no experiment until StartTrainingExperimentRun links it to one.

// InsertTrainedModelEvalRun stores a new eval run (status running unless e.Status says otherwise) and returns its ID, which is the ID of
// its gb_experiment_runs row. The ID, the creation time and the finish time are ignored; the counters and the judge's
// usage start as given (normally 0).
func InsertTrainedModelEvalRun(ctx context.Context, d *sql.DB, e model.TrainedModelEvalRun) (int64, error) {
	if e.Scheme == "" {
		e.Scheme = model.TrainingSchemeFineTuneDataset
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("insert trained model eval run: %w", err)
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO gb_experiment_runs (type, status, params, started_at) VALUES ($1, $2, $3::jsonb, $4) RETURNING id`,
		model.EvalTypeTraining, e.Status, e.ParamsJSON(), Now()).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert trained model eval run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gb_training_eval_run_meta (run_id, trained_model_id, scheme, dataset, dataset_sha256, judge_model, num_examples)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, e.TrainedModelID, e.Scheme, e.Dataset, e.DatasetSHA256, e.JudgeModel, e.NumExamples); err != nil {
		return 0, fmt.Errorf("insert trained model eval run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("insert trained model eval run: %w", err)
	}
	return id, nil
}

// SaveTrainedModelEvalRunBatch stores the cases of a finished batch (a case result and its metadata each, and the case in the run's
// experiment if it has one) and the eval run's counters after it (e.ID names the eval run; its judge model, counters, the judge's
// usage and cost are written), in one transaction. model.ErrNotFound if there is no such eval run.
func SaveTrainedModelEvalRunBatch(ctx context.Context, d *sql.DB, e model.TrainedModelEvalRun, cases []model.TrainedModelCaseResult) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save eval run batch: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE gb_training_eval_run_meta SET judge_model = $1, num_answered = $2, num_judged = $3, num_failed = $4, num_correct = $5,
			num_partial = $6, num_incorrect = $7, score_sum = $8, num_batches = $9, judge_calls = $10, judge_input_tokens = $11,
			judge_output_tokens = $12, judge_cost_usd = $13 WHERE run_id = $14`,
		e.JudgeModel, e.NumAnswered, e.NumJudged, e.NumFailed, e.NumCorrect, e.NumPartial, e.NumIncorrect, e.ScoreSum,
		e.NumBatches, e.JudgeCalls, e.JudgeInputTokens, e.JudgeOutputTokens, e.JudgeCostUSD, e.ID)
	if err != nil {
		return fmt.Errorf("save eval run batch: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	var eval string
	if err := tx.QueryRowContext(ctx, `SELECT dataset FROM gb_training_eval_run_meta WHERE run_id = $1`, e.ID).Scan(&eval); err != nil {
		return fmt.Errorf("save eval run batch: %w", err)
	}
	now := Now()
	for _, it := range cases {
		status := model.TrainedModelCaseJudged
		if it.Status != model.TrainedModelCaseJudged {
			status = model.TrainedModelCaseFailed
		}
		name := strconv.Itoa(it.Idx)
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO gb_experiment_case_results (type, run_id, eval, case_name, status, error, started_at, finished_at)
			VALUES ('training', $1, $2, $3, $4, $5, $6, $6) RETURNING id`, e.ID, eval, name, status, it.Error, now).Scan(&id); err != nil {
			return fmt.Errorf("save eval run case %d: %w", it.Idx, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_training_case_meta (case_result_id, idx, batch, instruction, input, reference, response, score, reason,
				input_tokens, output_tokens, duration_ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			id, it.Idx, it.Batch, it.Instruction, it.Input, it.Reference, it.Response, it.Score, it.Reason,
			it.InputTokens, it.OutputTokens, it.DurationMS); err != nil {
			return fmt.Errorf("save eval run case %d: %w", it.Idx, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_experiment_cases (experiment_id, type, eval, case_name, created_at)
			SELECT experiment_id, 'training', $1::text, $2::text, $3::timestamptz FROM gb_experiment_runs WHERE id = $4 AND experiment_id IS NOT NULL
			ON CONFLICT (experiment_id, eval, case_name) DO NOTHING`, eval, name, now, e.ID); err != nil {
			return fmt.Errorf("save eval run case %d: %w", it.Idx, err)
		}
	}
	return tx.Commit()
}

// finishTrainingRuns ends the running training run with that ID (every running training run for id 0) with the status, and the
// error in its metadata, and sets its case totals from the eval run's counters: total_cases are the cases of the eval,
// passed_cases the judged ones and failed_cases the others (failed, or not reached because the eval run was cancelled), and its
// cost the judge's. It returns how many runs it ended.
func finishTrainingRuns(ctx context.Context, q rower, id int64, status, errText string, now time.Time) (int64, error) {
	if _, err := q.ExecContext(ctx, `
		UPDATE gb_training_eval_run_meta SET error = $1
		WHERE run_id IN (SELECT id FROM gb_experiment_runs WHERE type = 'training' AND status = 'running' AND ($2::bigint = 0 OR id = $2))`,
		errText, id); err != nil {
		return 0, err
	}
	res, err := q.ExecContext(ctx, `
		UPDATE gb_experiment_runs
		SET status       = $1,
			total_cases  = (SELECT tm.num_examples FROM gb_training_eval_run_meta tm WHERE tm.run_id = gb_experiment_runs.id),
			passed_cases = (SELECT tm.num_judged FROM gb_training_eval_run_meta tm WHERE tm.run_id = gb_experiment_runs.id),
			failed_cases = (SELECT tm.num_examples - tm.num_judged FROM gb_training_eval_run_meta tm WHERE tm.run_id = gb_experiment_runs.id),
			cost_usd     = (SELECT tm.judge_cost_usd FROM gb_training_eval_run_meta tm WHERE tm.run_id = gb_experiment_runs.id),
			finished_at  = $2
		WHERE type = 'training' AND status = 'running' AND ($3::bigint = 0 OR id = $3)`, status, now, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FinishTrainedModelEvalRun ends a running eval run with the status and error given. An eval run that is not running is
// left as it is (it was finished already); model.ErrNotFound if there is no such eval run.
func FinishTrainedModelEvalRun(ctx context.Context, d *sql.DB, id int64, status, errText string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("finish trained model eval run: %w", err)
	}
	defer tx.Rollback()
	n, err := finishTrainingRuns(ctx, tx, id, status, errText, Now())
	if err != nil {
		return fmt.Errorf("finish trained model eval run: %w", err)
	}
	if n == 0 {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM gb_experiment_runs WHERE id = $1 AND type = 'training'`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return model.ErrNotFound
		}
		return nil
	}
	return tx.Commit()
}

// FailRunningTrainedModelEvalRuns sets every running eval run to failed with the reason, and returns how many there were.
func FailRunningTrainedModelEvalRuns(ctx context.Context, d *sql.DB, reason string) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("fail running trained model eval runs: %w", err)
	}
	defer tx.Rollback()
	n, err := finishTrainingRuns(ctx, tx, 0, model.TrainedModelRunFailed, reason, Now())
	if err != nil {
		return 0, fmt.Errorf("fail running trained model eval runs: %w", err)
	}
	return n, tx.Commit()
}

// trainedModelEvalRunColumns and trainedModelEvalRunFrom select a model.TrainedModelEvalRun (scanTrainedModelEvalRun): the run r (which holds the
// status, the params and the times) and its training metadata tm.
const trainedModelEvalRunColumns = `r.id, tm.trained_model_id, r.status, tm.scheme, tm.dataset, tm.dataset_sha256, r.params::text, tm.judge_model,
	tm.num_examples, tm.num_answered, tm.num_judged, tm.num_failed, tm.num_correct, tm.num_partial, tm.num_incorrect, tm.score_sum,
	tm.num_batches, tm.judge_calls, tm.judge_input_tokens, tm.judge_output_tokens, tm.judge_cost_usd, tm.error, r.started_at, r.finished_at`

const trainedModelEvalRunFrom = ` FROM gb_experiment_runs r
	JOIN gb_training_eval_run_meta tm ON tm.run_id = r.id`

func scanTrainedModelEvalRun(s rowScanner) (*model.TrainedModelEvalRun, error) {
	var e model.TrainedModelEvalRun
	var params string
	var finished sql.NullTime
	if err := s.Scan(&e.ID, &e.TrainedModelID, &e.Status, &e.Scheme, &e.Dataset, &e.DatasetSHA256, &params, &e.JudgeModel, &e.NumExamples,
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

// GetTrainedModelEvalRun returns the eval run with that ID, or model.ErrNotFound.
func GetTrainedModelEvalRun(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModelEvalRun, error) {
	e, err := scanTrainedModelEvalRun(d.QueryRowContext(ctx, `SELECT `+trainedModelEvalRunColumns+trainedModelEvalRunFrom+` WHERE r.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return e, err
}

// ListTrainedModelEvalRuns returns the newest eval runs first (of one trained model, or of all for modelID 0), at most limit (all for limit <= 0).
func ListTrainedModelEvalRuns(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]model.TrainedModelEvalRun, error) {
	q := `SELECT ` + trainedModelEvalRunColumns + trainedModelEvalRunFrom
	var args []any
	if modelID > 0 {
		args = append(args, modelID)
		q += fmt.Sprintf(` WHERE tm.trained_model_id = $%d`, len(args))
	}
	q += ` ORDER BY r.id DESC`
	if limit > 0 {
		args = append(args, limit)
		q += fmt.Sprintf(` LIMIT $%d`, len(args))
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list trained model eval runs: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelEvalRun{}
	for rows.Next() {
		e, err := scanTrainedModelEvalRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ListTrainedModelCaseResults returns the cases of an eval run stored so far, in dataset order.
func ListTrainedModelCaseResults(ctx context.Context, d *sql.DB, evalRunID int64) ([]model.TrainedModelCaseResult, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT e.id, e.run_id, cm.idx, cm.batch, cm.instruction, cm.input, cm.reference, cm.response,
			e.status, e.error, cm.score, cm.reason, cm.input_tokens,
			cm.output_tokens, cm.duration_ms
		FROM gb_experiment_case_results e JOIN gb_training_case_meta cm ON cm.case_result_id = e.id
		WHERE e.run_id = $1 ORDER BY cm.idx`, evalRunID)
	if err != nil {
		return nil, fmt.Errorf("list trained model eval run cases: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelCaseResult{}
	for rows.Next() {
		var it model.TrainedModelCaseResult
		var score sql.NullInt64
		if err := rows.Scan(&it.ID, &it.EvalRunID, &it.Idx, &it.Batch, &it.Instruction, &it.Input, &it.Reference, &it.Response,
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
