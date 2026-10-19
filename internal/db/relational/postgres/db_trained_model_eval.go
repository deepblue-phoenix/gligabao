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
// Suite runs of trained models: the 'training' rows of the universal eval tables (gb_experiment_runs and gb_experiment_case_results)
// with their metadata (gb_training_suite_run_meta) and cases (gb_training_case_results), see db_schema.go and sqlite/db_schema.go.
//
// A suite run (model.TrainedModelSuiteRun) is ONE RUN with ONE RESULT in it: its ID is that of the gb_experiment_case_results row, its
// status, error, times and judge cost are the columns of that row, its Params are those of the run, and the rest is in the
// metadata row. A run is in no experiment until StartTrainingExperimentRun links it to one.

// InsertTrainedModelSuiteRun stores a new suite run (status running unless e.Status says otherwise) and returns its ID, which is the ID of
// its gb_experiment_case_results row. The ID, the creation time and the finish time are ignored; the counters and the judge's
// usage start as given (normally 0).
func InsertTrainedModelSuiteRun(ctx context.Context, d *sql.DB, e model.TrainedModelSuiteRun) (int64, error) {
	if e.Scheme == "" {
		e.Scheme = model.TrainingSchemeFineTuneDataset
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("insert trained model suite run: %w", err)
	}
	defer tx.Rollback()
	now := Now()
	var runID, id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO gb_experiment_runs (type, status, params, started_at) VALUES ($1, $2, $3::jsonb, $4) RETURNING id`,
		model.EvalTypeTraining, e.Status, e.ParamsJSON(), now).Scan(&runID); err != nil {
		return 0, fmt.Errorf("insert trained model suite run: %w", err)
	}
	// The suite is the dataset; the result is that of the whole suite, so the case name is ''.
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO gb_experiment_case_results (type, run_id, suite, case_name, status, started_at) VALUES ($1, $2, $3, '', $4, $5) RETURNING id`,
		model.EvalTypeTraining, runID, e.Dataset, e.Status, now).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert trained model suite run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gb_training_suite_run_meta (case_result_id, trained_model_id, scheme, dataset, dataset_sha256, judge_model, num_examples)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, e.TrainedModelID, e.Scheme, e.Dataset, e.DatasetSHA256, e.JudgeModel, e.NumExamples); err != nil {
		return 0, fmt.Errorf("insert trained model suite run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("insert trained model suite run: %w", err)
	}
	return id, nil
}

// SaveTrainedModelSuiteRunBatch stores the cases of a finished batch and the suite run's counters after it (e.ID names the suite run; its
// judge model, counters and the judge's usage are written), in one transaction. model.ErrNotFound if there is no such suite run.
func SaveTrainedModelSuiteRunBatch(ctx context.Context, d *sql.DB, e model.TrainedModelSuiteRun, cases []model.TrainedModelCaseResult) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save suite run batch: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE gb_training_suite_run_meta SET judge_model = $1, num_answered = $2, num_judged = $3, num_failed = $4, num_correct = $5,
			num_partial = $6, num_incorrect = $7, score_sum = $8, num_batches = $9, judge_calls = $10, judge_input_tokens = $11,
			judge_output_tokens = $12 WHERE case_result_id = $13`,
		e.JudgeModel, e.NumAnswered, e.NumJudged, e.NumFailed, e.NumCorrect, e.NumPartial, e.NumIncorrect, e.ScoreSum,
		e.NumBatches, e.JudgeCalls, e.JudgeInputTokens, e.JudgeOutputTokens, e.ID)
	if err != nil {
		return fmt.Errorf("save suite run batch: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gb_experiment_case_results SET cost_usd = $1 WHERE id = $2`, e.JudgeCostUSD, e.ID); err != nil {
		return fmt.Errorf("save suite run batch: %w", err)
	}
	for _, it := range cases {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_training_case_results (suite_run_id, idx, batch, instruction, input, reference, response, status, error,
				score, reason, input_tokens, output_tokens, duration_ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			e.ID, it.Idx, it.Batch, it.Instruction, it.Input, it.Reference, it.Response, it.Status, it.Error,
			it.Score, it.Reason, it.InputTokens, it.OutputTokens, it.DurationMS); err != nil {
			return fmt.Errorf("save suite run case %d: %w", it.Idx, err)
		}
	}
	return tx.Commit()
}

// finishTrainingRuns ends the running training runs that have no running suite run left (the run of a suite run that was just finished),
// setting their status to the suite run's and their case totals from its counters: total_cases are the cases of the suite,
// passed_cases the judged ones and failed_cases the others (failed, or not reached because the suite run was cancelled).
func finishTrainingRuns(ctx context.Context, q rower, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		UPDATE gb_experiment_runs
		SET status       = (SELECT CASE WHEN e.status = 'error' THEN 'failed' ELSE e.status END FROM gb_experiment_case_results e
				WHERE e.run_id = gb_experiment_runs.id ORDER BY e.id LIMIT 1),
			total_cases  = (SELECT COALESCE(SUM(tm.num_examples), 0) FROM gb_experiment_case_results e
				JOIN gb_training_suite_run_meta tm ON tm.case_result_id = e.id WHERE e.run_id = gb_experiment_runs.id),
			passed_cases = (SELECT COALESCE(SUM(tm.num_judged), 0) FROM gb_experiment_case_results e
				JOIN gb_training_suite_run_meta tm ON tm.case_result_id = e.id WHERE e.run_id = gb_experiment_runs.id),
			failed_cases = (SELECT COALESCE(SUM(tm.num_examples - tm.num_judged), 0) FROM gb_experiment_case_results e
				JOIN gb_training_suite_run_meta tm ON tm.case_result_id = e.id WHERE e.run_id = gb_experiment_runs.id),
			cost_usd     = (SELECT COALESCE(SUM(e.cost_usd), 0) FROM gb_experiment_case_results e WHERE e.run_id = gb_experiment_runs.id),
			finished_at  = $1
		WHERE type = 'training' AND status = 'running'
			AND NOT EXISTS (SELECT 1 FROM gb_experiment_case_results e WHERE e.run_id = gb_experiment_runs.id AND e.status = 'running')`, now)
	return err
}

// FinishTrainedModelSuiteRun ends a running suite run with the status and error given, and with it its run. A suite run that is not running is
// left as it is (it was finished already); model.ErrNotFound if there is no such suite run.
func FinishTrainedModelSuiteRun(ctx context.Context, d *sql.DB, id int64, status, errText string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("finish trained model suite run: %w", err)
	}
	defer tx.Rollback()
	now := Now()
	res, err := tx.ExecContext(ctx, `
		UPDATE gb_experiment_case_results SET status = $1, error = $2, finished_at = $3
		WHERE id = $4 AND type = 'training' AND status = 'running'`, status, errText, now, id)
	if err != nil {
		return fmt.Errorf("finish trained model suite run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM gb_experiment_case_results WHERE id = $1 AND type = 'training'`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return model.ErrNotFound
		}
		return nil
	}
	if err := finishTrainingRuns(ctx, tx, now); err != nil {
		return fmt.Errorf("finish trained model suite run: %w", err)
	}
	return tx.Commit()
}

// FailRunningTrainedModelSuiteRuns sets every running suite run to failed with the reason (and ends its run), and returns how many there were.
func FailRunningTrainedModelSuiteRuns(ctx context.Context, d *sql.DB, reason string) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("fail running trained model suite runs: %w", err)
	}
	defer tx.Rollback()
	now := Now()
	res, err := tx.ExecContext(ctx, `
		UPDATE gb_experiment_case_results SET status = 'failed', error = $1, finished_at = $2
		WHERE type = 'training' AND status = 'running'`, reason, now)
	if err != nil {
		return 0, fmt.Errorf("fail running trained model suite runs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := finishTrainingRuns(ctx, tx, now); err != nil {
		return 0, fmt.Errorf("fail running trained model suite runs: %w", err)
	}
	return n, tx.Commit()
}

// trainedModelSuiteRunColumns and trainedModelSuiteRunFrom select a model.TrainedModelSuiteRun (scanTrainedModelSuiteRun): the result e, its
// training metadata tm and its run r (which holds the params).
const trainedModelSuiteRunColumns = `e.id, tm.trained_model_id, e.status, tm.scheme, tm.dataset, tm.dataset_sha256, r.params::text, tm.judge_model,
	tm.num_examples, tm.num_answered, tm.num_judged, tm.num_failed, tm.num_correct, tm.num_partial, tm.num_incorrect, tm.score_sum,
	tm.num_batches, tm.judge_calls, tm.judge_input_tokens, tm.judge_output_tokens, e.cost_usd, e.error, e.started_at, e.finished_at`

const trainedModelSuiteRunFrom = ` FROM gb_experiment_case_results e
	JOIN gb_training_suite_run_meta tm ON tm.case_result_id = e.id
	JOIN gb_experiment_runs r ON r.id = e.run_id`

func scanTrainedModelSuiteRun(s rowScanner) (*model.TrainedModelSuiteRun, error) {
	var e model.TrainedModelSuiteRun
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

// GetTrainedModelSuiteRun returns the suite run with that ID, or model.ErrNotFound.
func GetTrainedModelSuiteRun(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModelSuiteRun, error) {
	e, err := scanTrainedModelSuiteRun(d.QueryRowContext(ctx, `SELECT `+trainedModelSuiteRunColumns+trainedModelSuiteRunFrom+` WHERE e.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return e, err
}

// ListTrainedModelSuiteRuns returns the newest suite runs first (of one trained model, or of all for modelID 0), at most limit (all for limit <= 0).
func ListTrainedModelSuiteRuns(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]model.TrainedModelSuiteRun, error) {
	q := `SELECT ` + trainedModelSuiteRunColumns + trainedModelSuiteRunFrom
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
		return nil, fmt.Errorf("list trained model suite runs: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelSuiteRun{}
	for rows.Next() {
		e, err := scanTrainedModelSuiteRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ListTrainedModelCaseResults returns the cases of a suite run stored so far, in dataset order.
func ListTrainedModelCaseResults(ctx context.Context, d *sql.DB, suiteRunID int64) ([]model.TrainedModelCaseResult, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, suite_run_id, idx, batch, instruction, input, reference, response, status, error, score, reason, input_tokens,
			output_tokens, duration_ms FROM gb_training_case_results WHERE suite_run_id = $1 ORDER BY idx`, suiteRunID)
	if err != nil {
		return nil, fmt.Errorf("list trained model suite run cases: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelCaseResult{}
	for rows.Next() {
		var it model.TrainedModelCaseResult
		var score sql.NullInt64
		if err := rows.Scan(&it.ID, &it.SuiteRunID, &it.Idx, &it.Batch, &it.Instruction, &it.Input, &it.Reference, &it.Response,
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
