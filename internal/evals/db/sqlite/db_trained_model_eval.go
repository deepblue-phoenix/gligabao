package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	rsqlite "github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db/model"
)

//---------------------------------------------------------------------------------------------
// Eval runs of trained models: the 'training' rows of the universal eval tables (gb_experiment_runs and gb_experiment_case_results)
// with their metadata (gb_training_eval_run_meta and gb_training_case_meta), see db_schema.go.
//
// An eval run (model.TrainedModelRun) is ONE RUN: its ID is the run's, its status, times and Params are the columns of the
// gb_experiment_runs row, and the rest is in the run's metadata row. Each example of the eval is a case result of that run (a
// gb_experiment_case_results row with a gb_training_case_meta row). A run is in no experiment until StartTrainingExperimentRun links it to one.

// InsertTrainedModelEvalRun stores a new eval run (status running unless e.Status says otherwise) and returns its ID, which is the ID of
// its gb_experiment_runs row. The ID, the creation time and the finish time are ignored; the counters and the judge's
// usage start as given (normally 0).
func InsertTrainedModelEvalRun(ctx context.Context, d *sql.DB, e model.TrainedModelRun) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("insert trained model eval run: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO gb_experiment_runs (type, status, params, started_at) VALUES (?, ?, ?, ?)`,
		model.EvalTypeTraining, e.Status, e.ParamsJSON(), rsqlite.FormatTime(rsqlite.Now()))
	if err != nil {
		return 0, fmt.Errorf("insert trained model eval run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gb_training_eval_run_meta (run_id, trained_model_id, eval, judge_model, num_examples)
		VALUES (?, ?, ?, ?, ?)`,
		id, e.TrainedModelID, e.Eval, e.JudgeModel, e.NumExamples); err != nil {
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
func SaveTrainedModelEvalRunBatch(ctx context.Context, d *sql.DB, e model.TrainedModelRun, cases []model.TrainedModelCase) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save eval run batch: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE gb_training_eval_run_meta SET judge_model = ?, num_answered = ?, num_judged = ?, num_errored = ?, num_correct = ?,
			num_partial = ?, num_incorrect = ?, score_sum = ?, num_batches = ?, judge_calls = ?, judge_input_tokens = ?,
			judge_output_tokens = ?, judge_cost_usd = ? WHERE run_id = ?`,
		e.JudgeModel, e.NumAnswered, e.NumJudged, e.NumErrored, e.NumCorrect, e.NumPartial, e.NumIncorrect, e.ScoreSum,
		e.NumBatches, e.JudgeCalls, e.JudgeInputTokens, e.JudgeOutputTokens, e.JudgeCostUSD, e.ID)
	if err != nil {
		return fmt.Errorf("save eval run batch: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	var eval string
	if err := tx.QueryRowContext(ctx, `SELECT eval FROM gb_training_eval_run_meta WHERE run_id = ?`, e.ID).Scan(&eval); err != nil {
		return fmt.Errorf("save eval run batch: %w", err)
	}
	now := rsqlite.FormatTime(rsqlite.Now())
	for _, it := range cases {
		status := model.TrainedModelCaseJudged
		if it.Status != model.TrainedModelCaseJudged {
			status = model.TrainedModelCaseErrored
		}
		name := it.Name
		if name == "" {
			return fmt.Errorf("save eval run case %d: the case has no name", it.Idx)
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO gb_experiment_case_results (type, run_id, eval, case_name, status, error, started_at, finished_at)
			VALUES ('training', ?, ?, ?, ?, ?, ?, ?)`, e.ID, eval, name, status, it.Error, now, now)
		if err != nil {
			return fmt.Errorf("save eval run case %d: %w", it.Idx, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_training_case_meta (case_result_id, idx, batch, instruction, input, reference, response, score, reason,
				input_tokens, output_tokens, duration_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, it.Idx, it.Batch, it.Instruction, it.Input, it.Reference, it.Response, it.Score, it.Reason,
			it.InputTokens, it.OutputTokens, it.DurationMS); err != nil {
			return fmt.Errorf("save eval run case %d: %w", it.Idx, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_experiment_cases (experiment_id, type, eval, case_name, created_at)
			SELECT experiment_id, 'training', ?, ?, ? FROM gb_experiment_runs WHERE id = ? AND experiment_id IS NOT NULL
			ON CONFLICT (experiment_id, eval, case_name) DO NOTHING`, eval, name, now, e.ID); err != nil {
			return fmt.Errorf("save eval run case %d: %w", it.Idx, err)
		}
	}
	return tx.Commit()
}

// finishTrainingRuns ends the running training run with that ID (every running training run for id 0) with the status, and the
// error in its metadata, and sets its case totals from the eval run's counters: total_cases are the cases of the eval,
// judged_cases the judged ones and error_cases the ones that errored (passed_cases and failed_cases are the knowledge verdicts, so
// 0; the cases not reached because the eval run was cancelled are in none), and its cost the judge's. It returns how many runs it ended.
func finishTrainingRuns(ctx context.Context, q rower, id int64, status, errText, now string) (int64, error) {
	if _, err := q.ExecContext(ctx, `
		UPDATE gb_training_eval_run_meta SET error = ?
		WHERE run_id IN (SELECT id FROM gb_experiment_runs WHERE type = 'training' AND status = 'running' AND (? = 0 OR id = ?))`,
		errText, id, id); err != nil {
		return 0, err
	}
	res, err := q.ExecContext(ctx, `
		UPDATE gb_experiment_runs
		SET status       = ?,
			total_cases  = (SELECT tm.num_examples FROM gb_training_eval_run_meta tm WHERE tm.run_id = gb_experiment_runs.id),
			passed_cases = 0,
			failed_cases = 0,
			judged_cases = (SELECT tm.num_judged FROM gb_training_eval_run_meta tm WHERE tm.run_id = gb_experiment_runs.id),
			error_cases  = (SELECT tm.num_errored FROM gb_training_eval_run_meta tm WHERE tm.run_id = gb_experiment_runs.id),
			cost_usd     = (SELECT tm.judge_cost_usd FROM gb_training_eval_run_meta tm WHERE tm.run_id = gb_experiment_runs.id),
			finished_at  = ?
		WHERE type = 'training' AND status = 'running' AND (? = 0 OR id = ?)`, status, now, id, id)
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
	n, err := finishTrainingRuns(ctx, tx, id, status, errText, rsqlite.FormatTime(rsqlite.Now()))
	if err != nil {
		return fmt.Errorf("finish trained model eval run: %w", err)
	}
	if n == 0 {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM gb_experiment_runs WHERE id = ? AND type = 'training'`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return model.ErrNotFound
		}
		return nil
	}
	return tx.Commit()
}

// FailRunningTrainedModelEvalRuns sets every running eval run to error with the reason, and returns how many there were.
func FailRunningTrainedModelEvalRuns(ctx context.Context, d *sql.DB, reason string) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("fail running trained model eval runs: %w", err)
	}
	defer tx.Rollback()
	n, err := finishTrainingRuns(ctx, tx, 0, model.TrainedModelRunErrored, reason, rsqlite.FormatTime(rsqlite.Now()))
	if err != nil {
		return 0, fmt.Errorf("fail running trained model eval runs: %w", err)
	}
	return n, tx.Commit()
}

// trainedModelEvalRunColumns and trainedModelEvalRunFrom select a model.TrainedModelRun (scanTrainedModelEvalRun): the run r (which holds the
// status, the params and the times) and its training metadata tm.
const trainedModelEvalRunColumns = `r.id, tm.trained_model_id, r.status, tm.eval, r.params, tm.judge_model,
	tm.num_examples, tm.num_answered, tm.num_judged, tm.num_errored, tm.num_correct, tm.num_partial, tm.num_incorrect, tm.score_sum,
	tm.num_batches, tm.judge_calls, tm.judge_input_tokens, tm.judge_output_tokens, tm.judge_cost_usd, tm.error, r.started_at, r.finished_at`

const trainedModelEvalRunFrom = ` FROM gb_experiment_runs r
	JOIN gb_training_eval_run_meta tm ON tm.run_id = r.id`

func scanTrainedModelEvalRun(s rowScanner) (*model.TrainedModelRun, error) {
	e := model.TrainedModelRun{Kind: model.TrainedModelKindEval}
	var params, created string
	var finished sql.NullString
	if err := s.Scan(&e.ID, &e.TrainedModelID, &e.Status, &e.Eval, &params, &e.JudgeModel, &e.NumExamples,
		&e.NumAnswered, &e.NumJudged, &e.NumErrored, &e.NumCorrect, &e.NumPartial, &e.NumIncorrect, &e.ScoreSum, &e.NumBatches,
		&e.JudgeCalls, &e.JudgeInputTokens, &e.JudgeOutputTokens, &e.JudgeCostUSD, &e.Error, &created, &finished); err != nil {
		return nil, err
	}
	e.SetStoredParams(params)
	e.CreatedAt = parseStoredTime(created)
	if finished.Valid {
		e.FinishedAt = rsqlite.ParseTime(finished.String)
	}
	return &e, nil
}

// GetTrainedModelEvalRun returns the eval run with that ID, or model.ErrNotFound.
func GetTrainedModelEvalRun(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModelRun, error) {
	e, err := scanTrainedModelEvalRun(d.QueryRowContext(ctx, `SELECT `+trainedModelEvalRunColumns+trainedModelEvalRunFrom+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return e, err
}

// ListTrainedModelEvalRuns returns the newest eval runs first (of one trained model, or of all for modelID 0), at most limit (all for limit <= 0).
func ListTrainedModelEvalRuns(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]model.TrainedModelRun, error) {
	q := `SELECT ` + trainedModelEvalRunColumns + trainedModelEvalRunFrom
	var args []any
	if modelID > 0 {
		q += ` WHERE tm.trained_model_id = ?`
		args = append(args, modelID)
	}
	q += ` ORDER BY r.id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list trained model eval runs: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelRun{}
	for rows.Next() {
		e, err := scanTrainedModelEvalRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ListTrainedModelEvalCases returns the cases of an eval run stored so far, in dataset order.
func ListTrainedModelEvalCases(ctx context.Context, d *sql.DB, evalRunID int64) ([]model.TrainedModelCase, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT e.id, e.run_id, cm.idx, e.case_name, cm.batch, cm.instruction, cm.input, cm.reference, cm.response,
			e.status, e.error, cm.score, cm.reason, cm.input_tokens,
			cm.output_tokens, cm.duration_ms
		FROM gb_experiment_case_results e JOIN gb_training_case_meta cm ON cm.case_result_id = e.id
		WHERE e.run_id = ? ORDER BY cm.idx`, evalRunID)
	if err != nil {
		return nil, fmt.Errorf("list trained model eval run cases: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelCase{}
	for rows.Next() {
		var it model.TrainedModelCase
		var score sql.NullInt64
		if err := rows.Scan(&it.ID, &it.RunID, &it.Idx, &it.Name, &it.Batch, &it.Instruction, &it.Input, &it.Reference, &it.Response,
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
