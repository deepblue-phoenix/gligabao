package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Eval experiments

// rower is what *sql.DB and *sql.Tx share for running queries.
type rower interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// jsonObject marshals m as a JSON object ("{}" for nil).
func jsonObject(m map[string]any) string {
	if m == nil {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func parseObject(s string) map[string]any {
	m := map[string]any{}
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func jsonList(l []string) string {
	if l == nil {
		l = []string{}
	}
	b, _ := json.Marshal(l)
	return string(b)
}

func parseList(s string) []string {
	l := []string{}
	if err := json.Unmarshal([]byte(s), &l); err != nil || l == nil {
		return []string{}
	}
	return l
}

//---------------------------------------------------------------------------------------------

const experimentColumns = `id, name, description, config::text, created_at`

func scanExperiment(r scanner) (*model.Experiment, error) {
	var e model.Experiment
	var config string
	if err := r.Scan(&e.ID, &e.Name, &e.Description, &config, &e.CreatedAt); err != nil {
		return nil, err
	}
	e.Config = parseObject(config)
	e.CreatedAt = e.CreatedAt.UTC()
	return &e, nil
}

func experimentEvals(ctx context.Context, q rower, experimentID int64) ([]model.EvalRef, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT suite, eval_name
		FROM gb_experiment_evals
		WHERE experiment_id = $1
		ORDER BY suite, eval_name`, experimentID)
	if err != nil {
		return nil, fmt.Errorf("list experiment evals: %w", err)
	}
	defer rows.Close()
	out := []model.EvalRef{}
	for rows.Next() {
		var r model.EvalRef
		if err := rows.Scan(&r.Suite, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EnsureExperiment returns the experiment called name, creating it first (with the given description and config)
// if needed. An existing experiment is returned unchanged.
func EnsureExperiment(ctx context.Context, d *sql.DB, name, description string, config map[string]any) (*model.Experiment, error) {
	if _, err := d.ExecContext(ctx, `
		INSERT INTO gb_experiments (name, description, config, created_at)
		VALUES ($1, $2, $3::jsonb, $4)
		ON CONFLICT (name) DO NOTHING`,
		name, description, jsonObject(config), Now()); err != nil {
		return nil, fmt.Errorf("create experiment: %w", err)
	}
	return GetExperiment(ctx, d, name)
}

// GetExperiment returns the experiment called name, with its Evals, or model.ErrNotFound.
func GetExperiment(ctx context.Context, d *sql.DB, name string) (*model.Experiment, error) {
	e, err := scanExperiment(d.QueryRowContext(ctx, `
		SELECT `+experimentColumns+`
		FROM gb_experiments
		WHERE name = $1`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if e.Evals, err = experimentEvals(ctx, d, e.ID); err != nil {
		return nil, err
	}
	return e, nil
}

// ListExperiments returns all experiments, oldest first, without their Evals.
func ListExperiments(ctx context.Context, d *sql.DB) ([]model.Experiment, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+experimentColumns+`
		FROM gb_experiments
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list experiments: %w", err)
	}
	defer rows.Close()
	out := []model.Experiment{}
	for rows.Next() {
		e, err := scanExperiment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

//---------------------------------------------------------------------------------------------

const runColumns = `
	id, experiment_id, status, params::text, total_evals, passed_evals, failed_evals, cost_usd, started_at, finished_at`

func scanRun(r scanner) (*model.ExperimentRun, error) {
	var run model.ExperimentRun
	var params string
	var finished sql.NullTime
	if err := r.Scan(&run.ID, &run.ExperimentID, &run.Status, &params, &run.TotalEvals, &run.PassedEvals,
		&run.FailedEvals, &run.CostUSD, &run.StartedAt, &finished); err != nil {
		return nil, err
	}
	run.Params = parseObject(params)
	run.StartedAt = run.StartedAt.UTC()
	run.FinishedAt = utc(finished)
	return &run, nil
}

// StartExperimentRun starts a run of the experiment (status running). It returns model.ErrNotFound if the
// experiment does not exist.
func StartExperimentRun(ctx context.Context, d *sql.DB, experimentID int64, params map[string]any) (*model.ExperimentRun, error) {
	var id int64
	err := d.QueryRowContext(ctx, `
		INSERT INTO gb_experiment_runs (experiment_id, status, params, started_at)
		SELECT id, 'running', $1::jsonb, $2::timestamptz
		FROM gb_experiments
		WHERE id = $3
		RETURNING id`,
		jsonObject(params), Now(), experimentID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("start experiment run: %w", err)
	}
	return getRun(ctx, d, id)
}

func getRun(ctx context.Context, q rower, id int64) (*model.ExperimentRun, error) {
	run, err := scanRun(q.QueryRowContext(ctx, `
		SELECT `+runColumns+`
		FROM gb_experiment_runs
		WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return run, err
}

// RecordExperimentRunEval stores the result of one eval of a running run, with its trajectory references, and adds
// the eval to the experiment's evals. The trajectories are numbered in order; the returned copy has the IDs of the
// eval and its trajectories set (e.ID, e.RunID and the trajectories' ID, RunEvalID, Seq and CreatedAt are overwritten).
// It returns model.ErrNotFound if the run does not exist and model.ErrRunNotRunning if it is already finished.
func RecordExperimentRunEval(ctx context.Context, d *sql.DB, e model.ExperimentRunEval) (*model.ExperimentRunEval, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("record experiment eval: %w", err)
	}
	defer tx.Rollback()

	var experimentID int64
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT experiment_id, status
		FROM gb_experiment_runs
		WHERE id = $1`, e.RunID).Scan(&experimentID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("record experiment eval: %w", err)
	}
	if status != model.ExperimentRunning {
		return nil, model.ErrRunNotRunning
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO gb_experiment_run_evals (
			run_id, suite, eval_name, status, answer, problems, error, steps, cost_usd, started_at, finished_at
		)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11)
		RETURNING id`,
		e.RunID, e.Suite, e.Name, e.Status, e.Answer, jsonList(e.Problems), e.Error, e.Steps, e.CostUSD,
		Normalize(e.StartedAt), Normalize(e.FinishedAt)).Scan(&e.ID)
	if err != nil {
		return nil, fmt.Errorf("record experiment eval: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gb_experiment_evals (experiment_id, suite, eval_name, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (experiment_id, suite, eval_name) DO NOTHING`,
		experimentID, e.Suite, e.Name, Now()); err != nil {
		return nil, fmt.Errorf("record experiment eval: %w", err)
	}

	trajs := make([]model.ExperimentTrajectory, len(e.Trajectories))
	for i, t := range e.Trajectories {
		t.RunEvalID, t.Seq, t.CreatedAt = e.ID, i+1, Now()
		err := tx.QueryRowContext(ctx, `
			INSERT INTO gb_experiment_trajectories (
				run_eval_id, seq, kind, trajectory_id, agent_name, steps, cost_usd, path, created_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING id`,
			t.RunEvalID, t.Seq, t.Kind, t.TrajectoryID, t.AgentName, t.Steps, t.CostUSD, t.Path, t.CreatedAt).Scan(&t.ID)
		if err != nil {
			return nil, fmt.Errorf("record experiment trajectory: %w", err)
		}
		trajs[i] = t
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("record experiment eval: %w", err)
	}
	e.Trajectories = trajs
	return &e, nil
}

// FinishExperimentRun totals the evals recorded for the run and ends it: passed if every eval passed, otherwise
// failed. Finishing a run that is already finished changes nothing. It returns the stored run (without Evals),
// or model.ErrNotFound.
func FinishExperimentRun(ctx context.Context, d *sql.DB, runID int64) (*model.ExperimentRun, error) {
	if _, err := d.ExecContext(ctx, `
		UPDATE gb_experiment_runs
		SET total_evals  = (SELECT COUNT(*) FROM gb_experiment_run_evals WHERE run_id = gb_experiment_runs.id),
			passed_evals = (SELECT COUNT(*) FROM gb_experiment_run_evals WHERE run_id = gb_experiment_runs.id AND status = 'passed'),
			failed_evals = (SELECT COUNT(*) FROM gb_experiment_run_evals WHERE run_id = gb_experiment_runs.id AND status <> 'passed'),
			cost_usd     = (SELECT COALESCE(SUM(cost_usd), 0) FROM gb_experiment_run_evals WHERE run_id = gb_experiment_runs.id),
			status       = CASE
				WHEN EXISTS (SELECT 1 FROM gb_experiment_run_evals WHERE run_id = gb_experiment_runs.id AND status <> 'passed')
				THEN 'failed' ELSE 'passed' END,
			finished_at  = $1
		WHERE id = $2 AND status = 'running'`,
		Now(), runID); err != nil {
		return nil, fmt.Errorf("finish experiment run: %w", err)
	}
	return getRun(ctx, d, runID)
}

// ListExperimentRuns returns the runs of the experiment, oldest first, without their Evals.
func ListExperimentRuns(ctx context.Context, d *sql.DB, experimentID int64) ([]model.ExperimentRun, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+runColumns+`
		FROM gb_experiment_runs
		WHERE experiment_id = $1
		ORDER BY id`, experimentID)
	if err != nil {
		return nil, fmt.Errorf("list experiment runs: %w", err)
	}
	defer rows.Close()
	out := []model.ExperimentRun{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

const trajectoryColumns = `id, run_eval_id, seq, kind, trajectory_id, agent_name, steps, cost_usd, path, created_at`

func scanTrajectory(r scanner) (*model.ExperimentTrajectory, error) {
	var t model.ExperimentTrajectory
	if err := r.Scan(&t.ID, &t.RunEvalID, &t.Seq, &t.Kind, &t.TrajectoryID, &t.AgentName, &t.Steps, &t.CostUSD, &t.Path, &t.CreatedAt); err != nil {
		return nil, err
	}
	t.CreatedAt = t.CreatedAt.UTC()
	return &t, nil
}

// GetExperimentRun returns the run with all its evals and their trajectory references, or model.ErrNotFound.
func GetExperimentRun(ctx context.Context, d *sql.DB, runID int64) (*model.ExperimentRun, error) {
	run, err := getRun(ctx, d, runID)
	if err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `
		SELECT id, run_id, suite, eval_name, status, answer, problems::text, error, steps, cost_usd, started_at, finished_at
		FROM gb_experiment_run_evals
		WHERE run_id = $1
		ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list experiment run evals: %w", err)
	}
	defer rows.Close()
	run.Evals = []model.ExperimentRunEval{}
	byID := map[int64]int{}
	for rows.Next() {
		var e model.ExperimentRunEval
		var problems string
		if err := rows.Scan(&e.ID, &e.RunID, &e.Suite, &e.Name, &e.Status, &e.Answer, &problems, &e.Error, &e.Steps,
			&e.CostUSD, &e.StartedAt, &e.FinishedAt); err != nil {
			return nil, err
		}
		e.Problems = parseList(problems)
		e.StartedAt, e.FinishedAt = e.StartedAt.UTC(), e.FinishedAt.UTC()
		e.Trajectories = []model.ExperimentTrajectory{}
		byID[e.ID] = len(run.Evals)
		run.Evals = append(run.Evals, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	trows, err := d.QueryContext(ctx, `
		SELECT `+trajectoryColumns+`
		FROM gb_experiment_trajectories
		WHERE run_eval_id IN (SELECT id FROM gb_experiment_run_evals WHERE run_id = $1)
		ORDER BY run_eval_id, seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("list experiment trajectories: %w", err)
	}
	defer trows.Close()
	for trows.Next() {
		t, err := scanTrajectory(trows)
		if err != nil {
			return nil, err
		}
		i := byID[t.RunEvalID]
		run.Evals[i].Trajectories = append(run.Evals[i].Trajectories, *t)
	}
	return run, trows.Err()
}

// FindExperimentTrajectory returns the reference to the trajectory with the given ATIF trajectory_id, or
// model.ErrNotFound. Its RunEvalID leads to the eval, and through it to the run and experiment.
func FindExperimentTrajectory(ctx context.Context, d *sql.DB, trajectoryID string) (*model.ExperimentTrajectory, error) {
	t, err := scanTrajectory(d.QueryRowContext(ctx, `
		SELECT `+trajectoryColumns+`
		FROM gb_experiment_trajectories
		WHERE trajectory_id = $1
		ORDER BY id
		LIMIT 1`, trajectoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return t, err
}
