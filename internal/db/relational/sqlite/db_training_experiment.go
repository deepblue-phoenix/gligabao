package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Training experiments (gb_training_experiments and gb_training_experiment_runs, see db_schema.go)

const trainingExperimentColumns = `x.id, x.name, x.description, x.config, x.created_at,
	(SELECT COUNT(*) FROM gb_training_experiment_runs r WHERE r.experiment_id = x.id)`

func scanTrainingExperiment(s rowScanner) (*model.TrainingExperiment, error) {
	var e model.TrainingExperiment
	var config, created string
	if err := s.Scan(&e.ID, &e.Name, &e.Description, &config, &created, &e.NumRuns); err != nil {
		return nil, err
	}
	e.Config = parseObject(config)
	e.CreatedAt = parseStoredTime(created)
	return &e, nil
}

// EnsureTrainingExperiment returns the training experiment called name, creating it first (with the description and config)
// if needed. An existing experiment is returned unchanged.
func EnsureTrainingExperiment(ctx context.Context, d *sql.DB, name, description string, config map[string]any) (*model.TrainingExperiment, error) {
	if _, err := d.ExecContext(ctx, `
		INSERT INTO gb_training_experiments (name, description, config, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO NOTHING`,
		name, description, jsonObject(config), FormatTime(Now())); err != nil {
		return nil, fmt.Errorf("create training experiment: %w", err)
	}
	e, err := scanTrainingExperiment(d.QueryRowContext(ctx, `
		SELECT `+trainingExperimentColumns+` FROM gb_training_experiments x WHERE x.name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return e, err
}

// GetTrainingExperiment returns the training experiment with that ID, or model.ErrNotFound.
func GetTrainingExperiment(ctx context.Context, d *sql.DB, id int64) (*model.TrainingExperiment, error) {
	e, err := scanTrainingExperiment(d.QueryRowContext(ctx, `
		SELECT `+trainingExperimentColumns+` FROM gb_training_experiments x WHERE x.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return e, err
}

// ListTrainingExperiments returns all training experiments, newest first.
func ListTrainingExperiments(ctx context.Context, d *sql.DB) ([]model.TrainingExperiment, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+trainingExperimentColumns+` FROM gb_training_experiments x ORDER BY x.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list training experiments: %w", err)
	}
	defer rows.Close()
	out := []model.TrainingExperiment{}
	for rows.Next() {
		e, err := scanTrainingExperiment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

const trainingExperimentRunSelect = `
	SELECT r.id, r.experiment_id, r.eval_id, r.created_at,
		e.id, e.trained_model_id, e.status, e.dataset, e.dataset_sha256, e.params, e.judge_model, e.num_examples,
		e.num_answered, e.num_judged, e.num_failed, e.num_correct, e.num_partial, e.num_incorrect, e.score_sum, e.num_batches,
		e.judge_calls, e.judge_input_tokens, e.judge_output_tokens, e.judge_cost_usd, e.error, e.created_at, e.finished_at,
		m.id, m.name, m.base_model, m.model_size, m.num_train_steps, m.final_loss, m.params
	FROM gb_training_experiment_runs r
	JOIN gb_trained_model_evals e ON e.id = r.eval_id
	JOIN gb_trained_models m ON m.id = e.trained_model_id`

func scanTrainingExperimentRun(s rowScanner) (*model.TrainingExperimentRun, error) {
	var r model.TrainingExperimentRun
	var created, evalParams, evalCreated, modelParams string
	var evalFinished sql.NullString
	var loss sql.NullFloat64
	e, m := &r.Eval, &r.Model
	if err := s.Scan(&r.ID, &r.ExperimentID, &r.EvalID, &created,
		&e.ID, &e.TrainedModelID, &e.Status, &e.Dataset, &e.DatasetSHA256, &evalParams, &e.JudgeModel, &e.NumExamples,
		&e.NumAnswered, &e.NumJudged, &e.NumFailed, &e.NumCorrect, &e.NumPartial, &e.NumIncorrect, &e.ScoreSum, &e.NumBatches,
		&e.JudgeCalls, &e.JudgeInputTokens, &e.JudgeOutputTokens, &e.JudgeCostUSD, &e.Error, &evalCreated, &evalFinished,
		&m.ID, &m.Name, &m.BaseModel, &m.ModelSize, &m.NumTrainSteps, &loss, &modelParams); err != nil {
		return nil, err
	}
	r.CreatedAt = parseStoredTime(created)
	e.SetStoredParams(evalParams)
	e.CreatedAt = parseStoredTime(evalCreated)
	if evalFinished.Valid {
		e.FinishedAt = ParseTime(evalFinished.String)
	}
	if loss.Valid {
		m.FinalLoss = &loss.Float64
	}
	m.SetStoredParams(modelParams)
	r.Summarize()
	return &r, nil
}

// StartTrainingExperimentRun makes the eval (gb_trained_model_evals) a run of the experiment and returns the run. It returns
// model.ErrNotFound if the experiment or the eval does not exist; an eval can be the run of one experiment only.
func StartTrainingExperimentRun(ctx context.Context, d *sql.DB, experimentID, evalID int64) (*model.TrainingExperimentRun, error) {
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_training_experiment_runs (experiment_id, eval_id, created_at)
		SELECT x.id, e.id, ?
		FROM gb_training_experiments x, gb_trained_model_evals e
		WHERE x.id = ? AND e.id = ?`,
		FormatTime(Now()), experimentID, evalID)
	if err != nil {
		return nil, fmt.Errorf("start training experiment run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, model.ErrNotFound
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetTrainingExperimentRun(ctx, d, id)
}

// GetTrainingExperimentRun returns the run with that ID, or model.ErrNotFound.
func GetTrainingExperimentRun(ctx context.Context, d *sql.DB, id int64) (*model.TrainingExperimentRun, error) {
	r, err := scanTrainingExperimentRun(d.QueryRowContext(ctx, trainingExperimentRunSelect+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return r, err
}

// ListTrainingExperimentRuns returns the runs of the experiment, oldest first.
func ListTrainingExperimentRuns(ctx context.Context, d *sql.DB, experimentID int64) ([]model.TrainingExperimentRun, error) {
	rows, err := d.QueryContext(ctx, trainingExperimentRunSelect+` WHERE r.experiment_id = ? ORDER BY r.id`, experimentID)
	if err != nil {
		return nil, fmt.Errorf("list training experiment runs: %w", err)
	}
	defer rows.Close()
	out := []model.TrainingExperimentRun{}
	for rows.Next() {
		r, err := scanTrainingExperimentRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}
