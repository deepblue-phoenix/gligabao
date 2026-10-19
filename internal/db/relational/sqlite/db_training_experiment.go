package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Training experiments: the 'training' rows of gb_experiments and gb_experiment_runs (see db_schema.go). A training run is a
// gb_experiment_runs row with ONE eval run (db_trained_model_eval.go); it is made with the eval run and joins an experiment later.

const trainingExperimentColumns = `x.id, x.name, x.description, x.config, x.created_at,
	(SELECT COUNT(*) FROM gb_experiment_runs r WHERE r.experiment_id = x.id)`

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
		INSERT INTO gb_experiments (type, name, description, config, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (type, name) DO NOTHING`,
		model.EvalTypeTraining, name, description, jsonObject(config), FormatTime(Now())); err != nil {
		return nil, fmt.Errorf("create training experiment: %w", err)
	}
	e, err := scanTrainingExperiment(d.QueryRowContext(ctx, `
		SELECT `+trainingExperimentColumns+` FROM gb_experiments x WHERE x.type = ? AND x.name = ?`, model.EvalTypeTraining, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return e, err
}

// GetTrainingExperiment returns the training experiment with that ID, or model.ErrNotFound.
func GetTrainingExperiment(ctx context.Context, d *sql.DB, id int64) (*model.TrainingExperiment, error) {
	e, err := scanTrainingExperiment(d.QueryRowContext(ctx, `
		SELECT `+trainingExperimentColumns+` FROM gb_experiments x WHERE x.type = ? AND x.id = ?`, model.EvalTypeTraining, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return e, err
}

// ListTrainingExperiments returns all training experiments, newest first.
func ListTrainingExperiments(ctx context.Context, d *sql.DB) ([]model.TrainingExperiment, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+trainingExperimentColumns+` FROM gb_experiments x WHERE x.type = ? ORDER BY x.id DESC`, model.EvalTypeTraining)
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
	SELECT r.id, COALESCE(r.experiment_id, 0), r.started_at,
		` + trainedModelEvalRunColumns + `,
		m.id, m.name, m.base_model, m.model_size, m.num_train_steps, m.final_loss, m.params
	FROM gb_experiment_runs r
	JOIN gb_training_eval_run_meta tm ON tm.run_id = r.id
	JOIN gb_trained_models m ON m.id = tm.trained_model_id`

func scanTrainingExperimentRun(s rowScanner) (*model.TrainingExperimentRun, error) {
	var r model.TrainingExperimentRun
	var created, runParams, runCreated, modelParams string
	var runFinished sql.NullString
	var loss sql.NullFloat64
	e, m := &r.EvalRun, &r.Model
	e.Kind = model.TrainedModelKindEval
	if err := s.Scan(&r.ID, &r.ExperimentID, &created,
		&e.ID, &e.TrainedModelID, &e.Status, &e.Eval, &runParams, &e.JudgeModel, &e.NumExamples,
		&e.NumAnswered, &e.NumJudged, &e.NumErrored, &e.NumCorrect, &e.NumPartial, &e.NumIncorrect, &e.ScoreSum, &e.NumBatches,
		&e.JudgeCalls, &e.JudgeInputTokens, &e.JudgeOutputTokens, &e.JudgeCostUSD, &e.Error, &runCreated, &runFinished,
		&m.ID, &m.Name, &m.BaseModel, &m.ModelSize, &m.NumTrainSteps, &loss, &modelParams); err != nil {
		return nil, err
	}
	r.CreatedAt = parseStoredTime(created)
	e.SetStoredParams(runParams)
	e.CreatedAt = parseStoredTime(runCreated)
	if runFinished.Valid {
		e.FinishedAt = ParseTime(runFinished.String)
	}
	if loss.Valid {
		m.FinalLoss = &loss.Float64
	}
	m.SetStoredParams(modelParams)
	r.Summarize()
	return &r, nil
}

// StartTrainingExperimentRun makes the eval run (its run) part of the experiment and returns the run: the run's experiment is set
// and the cases the eval run has so far are added to the experiment's cases (gb_experiment_cases; the later ones are added as they are saved). It returns model.ErrNotFound if the experiment or the
// eval run does not exist, and an error if the eval run is in an experiment already (an eval run can be in one experiment only).
func StartTrainingExperimentRun(ctx context.Context, d *sql.DB, experimentID, evalRunID int64) (*model.TrainingExperimentRun, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("start training experiment run: %w", err)
	}
	defer tx.Rollback()
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM gb_experiments WHERE id = ? AND type = 'training'`, experimentID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("start training experiment run: %w", err)
	}
	runID := evalRunID
	var inExperiment sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT experiment_id FROM gb_experiment_runs WHERE id = ? AND type = 'training'`, evalRunID).Scan(&inExperiment)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("start training experiment run: %w", err)
	}
	if inExperiment.Valid {
		return nil, fmt.Errorf("start training experiment run: eval run %d is in experiment %d already", evalRunID, inExperiment.Int64)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gb_experiment_runs SET experiment_id = ? WHERE id = ?`, experimentID, runID); err != nil {
		return nil, fmt.Errorf("start training experiment run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gb_experiment_cases (experiment_id, type, eval, case_name, created_at)
		SELECT ?, 'training', eval, case_name, ? FROM gb_experiment_case_results WHERE run_id = ?
		ON CONFLICT (experiment_id, eval, case_name) DO NOTHING`,
		experimentID, FormatTime(Now()), runID); err != nil {
		return nil, fmt.Errorf("start training experiment run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("start training experiment run: %w", err)
	}
	return GetTrainingExperimentRun(ctx, d, runID)
}

// GetTrainingExperimentRun returns the run with that ID, or model.ErrNotFound.
func GetTrainingExperimentRun(ctx context.Context, d *sql.DB, id int64) (*model.TrainingExperimentRun, error) {
	r, err := scanTrainingExperimentRun(d.QueryRowContext(ctx, trainingExperimentRunSelect+` WHERE r.type = 'training' AND r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return r, err
}

// ListTrainingExperimentRuns returns the runs of the experiment, oldest first.
func ListTrainingExperimentRuns(ctx context.Context, d *sql.DB, experimentID int64) ([]model.TrainingExperimentRun, error) {
	rows, err := d.QueryContext(ctx, trainingExperimentRunSelect+` WHERE r.type = 'training' AND r.experiment_id = ? ORDER BY r.id`, experimentID)
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
