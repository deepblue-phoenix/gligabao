package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Suite runs of trained models: the 'training' rows of the universal eval tables (gb_experiment_runs and gb_experiment_case_results)
// with their metadata (gb_training_suite_run_meta) and cases (gb_training_case_results), see db_schema.go.
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
	now := FormatTime(Now())
	res, err := tx.ExecContext(ctx, `
		INSERT INTO gb_experiment_runs (type, status, params, started_at) VALUES (?, ?, ?, ?)`,
		model.EvalTypeTraining, e.Status, e.ParamsJSON(), now)
	if err != nil {
		return 0, fmt.Errorf("insert trained model suite run: %w", err)
	}
	runID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// The suite is the dataset; the result is that of the whole suite, so the case name is ''.
	res, err = tx.ExecContext(ctx, `
		INSERT INTO gb_experiment_case_results (type, run_id, suite, case_name, status, started_at) VALUES (?, ?, ?, '', ?, ?)`,
		model.EvalTypeTraining, runID, e.Dataset, e.Status, now)
	if err != nil {
		return 0, fmt.Errorf("insert trained model suite run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gb_training_suite_run_meta (case_result_id, trained_model_id, scheme, dataset, dataset_sha256, judge_model, num_examples)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
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
		UPDATE gb_training_suite_run_meta SET judge_model = ?, num_answered = ?, num_judged = ?, num_failed = ?, num_correct = ?,
			num_partial = ?, num_incorrect = ?, score_sum = ?, num_batches = ?, judge_calls = ?, judge_input_tokens = ?,
			judge_output_tokens = ? WHERE case_result_id = ?`,
		e.JudgeModel, e.NumAnswered, e.NumJudged, e.NumFailed, e.NumCorrect, e.NumPartial, e.NumIncorrect, e.ScoreSum,
		e.NumBatches, e.JudgeCalls, e.JudgeInputTokens, e.JudgeOutputTokens, e.ID)
	if err != nil {
		return fmt.Errorf("save suite run batch: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gb_experiment_case_results SET cost_usd = ? WHERE id = ?`, e.JudgeCostUSD, e.ID); err != nil {
		return fmt.Errorf("save suite run batch: %w", err)
	}
	for _, it := range cases {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_training_case_results (suite_run_id, idx, batch, instruction, input, reference, response, status, error,
				score, reason, input_tokens, output_tokens, duration_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
func finishTrainingRuns(ctx context.Context, q rower, now string) error {
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
			finished_at  = ?
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
	now := FormatTime(Now())
	res, err := tx.ExecContext(ctx, `
		UPDATE gb_experiment_case_results SET status = ?, error = ?, finished_at = ?
		WHERE id = ? AND type = 'training' AND status = 'running'`, status, errText, now, id)
	if err != nil {
		return fmt.Errorf("finish trained model suite run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM gb_experiment_case_results WHERE id = ? AND type = 'training'`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
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
	now := FormatTime(Now())
	res, err := tx.ExecContext(ctx, `
		UPDATE gb_experiment_case_results SET status = 'failed', error = ?, finished_at = ?
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
const trainedModelSuiteRunColumns = `e.id, tm.trained_model_id, e.status, tm.scheme, tm.dataset, tm.dataset_sha256, r.params, tm.judge_model,
	tm.num_examples, tm.num_answered, tm.num_judged, tm.num_failed, tm.num_correct, tm.num_partial, tm.num_incorrect, tm.score_sum,
	tm.num_batches, tm.judge_calls, tm.judge_input_tokens, tm.judge_output_tokens, e.cost_usd, e.error, e.started_at, e.finished_at`

const trainedModelSuiteRunFrom = ` FROM gb_experiment_case_results e
	JOIN gb_training_suite_run_meta tm ON tm.case_result_id = e.id
	JOIN gb_experiment_runs r ON r.id = e.run_id`

func scanTrainedModelSuiteRun(s rowScanner) (*model.TrainedModelSuiteRun, error) {
	var e model.TrainedModelSuiteRun
	var params, created string
	var finished sql.NullString
	if err := s.Scan(&e.ID, &e.TrainedModelID, &e.Status, &e.Scheme, &e.Dataset, &e.DatasetSHA256, &params, &e.JudgeModel, &e.NumExamples,
		&e.NumAnswered, &e.NumJudged, &e.NumFailed, &e.NumCorrect, &e.NumPartial, &e.NumIncorrect, &e.ScoreSum, &e.NumBatches,
		&e.JudgeCalls, &e.JudgeInputTokens, &e.JudgeOutputTokens, &e.JudgeCostUSD, &e.Error, &created, &finished); err != nil {
		return nil, err
	}
	e.SetStoredParams(params)
	e.CreatedAt = parseStoredTime(created)
	if finished.Valid {
		e.FinishedAt = ParseTime(finished.String)
	}
	return &e, nil
}

// GetTrainedModelSuiteRun returns the suite run with that ID, or model.ErrNotFound.
func GetTrainedModelSuiteRun(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModelSuiteRun, error) {
	e, err := scanTrainedModelSuiteRun(d.QueryRowContext(ctx, `SELECT `+trainedModelSuiteRunColumns+trainedModelSuiteRunFrom+` WHERE e.id = ?`, id))
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
		q += ` WHERE tm.trained_model_id = ?`
		args = append(args, modelID)
	}
	q += ` ORDER BY e.id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
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
			output_tokens, duration_ms FROM gb_training_case_results WHERE suite_run_id = ? ORDER BY idx`, suiteRunID)
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
