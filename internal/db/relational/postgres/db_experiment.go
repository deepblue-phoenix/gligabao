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

func jsonIDs(ids []int64) string {
	if ids == nil {
		ids = []int64{}
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

func parseIDs(s string) []int64 {
	ids := []int64{}
	if err := json.Unmarshal([]byte(s), &ids); err != nil || ids == nil {
		return []int64{}
	}
	return ids
}

//---------------------------------------------------------------------------------------------

const experimentColumns = `id, type, name, description, config::text, created_at`

func scanExperiment(r scanner) (*model.Experiment, error) {
	var e model.Experiment
	var config string
	if err := r.Scan(&e.ID, &e.Type, &e.Name, &e.Description, &config, &e.CreatedAt); err != nil {
		return nil, err
	}
	e.Config = parseObject(config)
	e.CreatedAt = e.CreatedAt.UTC()
	return &e, nil
}

func experimentCases(ctx context.Context, q rower, experimentID int64) ([]model.CaseRef, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT eval, case_name
		FROM gb_experiment_cases
		WHERE experiment_id = $1
		ORDER BY eval, case_name`, experimentID)
	if err != nil {
		return nil, fmt.Errorf("list experiment cases: %w", err)
	}
	defer rows.Close()
	out := []model.CaseRef{}
	for rows.Next() {
		var r model.CaseRef
		if err := rows.Scan(&r.Eval, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EnsureExperiment returns the knowledge experiment called name, creating it first (with the given description and config)
// if needed. An existing experiment is returned unchanged.
func EnsureExperiment(ctx context.Context, d *sql.DB, name, description string, config map[string]any) (*model.Experiment, error) {
	if _, err := d.ExecContext(ctx, `
		INSERT INTO gb_experiments (type, name, description, config, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5)
		ON CONFLICT (type, name) DO NOTHING`,
		model.EvalTypeKnowledge, name, description, jsonObject(config), Now()); err != nil {
		return nil, fmt.Errorf("create experiment: %w", err)
	}
	return GetExperiment(ctx, d, name)
}

// GetExperiment returns the knowledge experiment called name, with its Cases, or model.ErrNotFound.
func GetExperiment(ctx context.Context, d *sql.DB, name string) (*model.Experiment, error) {
	e, err := scanExperiment(d.QueryRowContext(ctx, `
		SELECT `+experimentColumns+`
		FROM gb_experiments
		WHERE type = $1 AND name = $2`, model.EvalTypeKnowledge, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if e.Cases, err = experimentCases(ctx, d, e.ID); err != nil {
		return nil, err
	}
	return e, nil
}

// ListExperiments returns all knowledge experiments, oldest first, without their Cases.
func ListExperiments(ctx context.Context, d *sql.DB) ([]model.Experiment, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+experimentColumns+`
		FROM gb_experiments
		WHERE type = $1
		ORDER BY id`, model.EvalTypeKnowledge)
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
	id, type, COALESCE(experiment_id, 0), status, params::text, total_cases, passed_cases, failed_cases, cost_usd, started_at, finished_at`

func scanRun(r scanner) (*model.ExperimentRun, error) {
	var run model.ExperimentRun
	var params string
	var finished sql.NullTime
	if err := r.Scan(&run.ID, &run.Type, &run.ExperimentID, &run.Status, &params, &run.TotalCases, &run.PassedCases,
		&run.FailedCases, &run.CostUSD, &run.StartedAt, &finished); err != nil {
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
		INSERT INTO gb_experiment_runs (type, experiment_id, status, params, started_at)
		SELECT type, id, 'running', $1::jsonb, $2::timestamptz
		FROM gb_experiments
		WHERE id = $3 AND type = $4
		RETURNING id`,
		jsonObject(params), Now(), experimentID, model.EvalTypeKnowledge).Scan(&id)
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
		WHERE id = $1 AND type = $2`, id, model.EvalTypeKnowledge))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return run, err
}

// RecordExperimentCaseResult stores the result of one case of a running run, with its trajectory references, and adds
// the case to the experiment's cases. The trajectories are numbered in order; the returned copy has the IDs of the
// case and its trajectories set (e.ID, e.RunID and the trajectories' ID, CaseResultID, Seq and CreatedAt are overwritten).
// It returns model.ErrNotFound if the run does not exist and model.ErrRunNotRunning if it is already finished.
func RecordExperimentCaseResult(ctx context.Context, d *sql.DB, e model.ExperimentCaseResult) (*model.ExperimentCaseResult, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("record experiment case: %w", err)
	}
	defer tx.Rollback()

	var experimentID int64
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT experiment_id, status
		FROM gb_experiment_runs
		WHERE id = $1 AND type = $2`, e.RunID, model.EvalTypeKnowledge).Scan(&experimentID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("record experiment case: %w", err)
	}
	if status != model.ExperimentRunning {
		return nil, model.ErrRunNotRunning
	}

	e.Type = model.EvalTypeKnowledge
	err = tx.QueryRowContext(ctx, `
		INSERT INTO gb_experiment_case_results (type, run_id, eval, case_name, status, error, cost_usd, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id`,
		e.Type, e.RunID, e.Eval, e.Name, e.Status, e.Error, e.CostUSD, Normalize(e.StartedAt), Normalize(e.FinishedAt)).Scan(&e.ID)
	if err != nil {
		return nil, fmt.Errorf("record experiment case: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gb_knowledge_case_meta (case_result_id, answer, problems, passed_checks, dataset_id, steps)
		VALUES ($1, $2, $3::jsonb, $4::jsonb, $5, $6)`,
		e.ID, e.Answer, jsonList(e.Problems), jsonList(e.PassedChecks), e.DatasetID, e.Steps); err != nil {
		return nil, fmt.Errorf("record experiment case: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gb_experiment_cases (experiment_id, type, eval, case_name, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (experiment_id, eval, case_name) DO NOTHING`,
		experimentID, model.EvalTypeKnowledge, e.Eval, e.Name, Now()); err != nil {
		return nil, fmt.Errorf("record experiment case: %w", err)
	}

	trajs := make([]model.ExperimentTrajectory, len(e.Trajectories))
	for i, t := range e.Trajectories {
		t.CaseResultID, t.Seq, t.CreatedAt = e.ID, i+1, Now()
		err := tx.QueryRowContext(ctx, `
			INSERT INTO gb_experiment_trajectories (
				case_result_id, seq, kind, trajectory_id, agent_name, steps, cost_usd, path, artifact_ids, created_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)
			RETURNING id`,
			t.CaseResultID, t.Seq, t.Kind, t.TrajectoryID, t.AgentName, t.Steps, t.CostUSD, t.Path, jsonIDs(t.ArtifactIDs), t.CreatedAt).Scan(&t.ID)
		if err != nil {
			return nil, fmt.Errorf("record experiment trajectory: %w", err)
		}
		trajs[i] = t
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("record experiment case: %w", err)
	}
	e.Trajectories = trajs
	return &e, nil
}

// FinishExperimentRun totals the cases recorded for the run and ends it: passed if every case passed, otherwise
// failed. Finishing a run that is already finished changes nothing. It returns the stored run (without Cases),
// or model.ErrNotFound.
func FinishExperimentRun(ctx context.Context, d *sql.DB, runID int64) (*model.ExperimentRun, error) {
	if _, err := d.ExecContext(ctx, `
		UPDATE gb_experiment_runs
		SET total_cases  = (SELECT COUNT(*) FROM gb_experiment_case_results WHERE run_id = gb_experiment_runs.id),
			passed_cases = (SELECT COUNT(*) FROM gb_experiment_case_results WHERE run_id = gb_experiment_runs.id AND status = 'passed'),
			failed_cases = (SELECT COUNT(*) FROM gb_experiment_case_results WHERE run_id = gb_experiment_runs.id AND status <> 'passed'),
			cost_usd     = (SELECT COALESCE(SUM(cost_usd), 0) FROM gb_experiment_case_results WHERE run_id = gb_experiment_runs.id),
			status       = CASE
				WHEN EXISTS (SELECT 1 FROM gb_experiment_case_results WHERE run_id = gb_experiment_runs.id AND status <> 'passed')
				THEN 'failed' ELSE 'passed' END,
			finished_at  = $1
		WHERE id = $2 AND type = $3 AND status = 'running'`,
		Now(), runID, model.EvalTypeKnowledge); err != nil {
		return nil, fmt.Errorf("finish experiment run: %w", err)
	}
	return getRun(ctx, d, runID)
}

// ListExperimentRuns returns the runs of the experiment, oldest first, without their Cases.
func ListExperimentRuns(ctx context.Context, d *sql.DB, experimentID int64) ([]model.ExperimentRun, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+runColumns+`
		FROM gb_experiment_runs
		WHERE experiment_id = $1 AND type = $2
		ORDER BY id`, experimentID, model.EvalTypeKnowledge)
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

const trajectoryColumns = `id, case_result_id, seq, kind, trajectory_id, agent_name, steps, cost_usd, path, artifact_ids::text, created_at`

func scanTrajectory(r scanner) (*model.ExperimentTrajectory, error) {
	var t model.ExperimentTrajectory
	var artifactIDs string
	if err := r.Scan(&t.ID, &t.CaseResultID, &t.Seq, &t.Kind, &t.TrajectoryID, &t.AgentName, &t.Steps, &t.CostUSD, &t.Path, &artifactIDs, &t.CreatedAt); err != nil {
		return nil, err
	}
	t.ArtifactIDs = parseIDs(artifactIDs)
	t.CreatedAt = t.CreatedAt.UTC()
	return &t, nil
}

// caseResultColumns are the columns scanCaseResult reads (gb_experiment_case_results as e, gb_knowledge_case_meta as k).
const caseResultColumns = `e.id, e.type, e.run_id, e.eval, e.case_name, e.status, k.answer, k.problems::text, k.passed_checks::text, k.dataset_id,
	e.error, k.steps, e.cost_usd, e.started_at, e.finished_at`

// scanCaseResult reads a result selected with caseResultColumns; its Trajectories are empty.
func scanCaseResult(r scanner) (*model.ExperimentCaseResult, error) {
	var e model.ExperimentCaseResult
	var problems, passed string
	var datasetID sql.NullInt64
	var finished sql.NullTime
	if err := r.Scan(&e.ID, &e.Type, &e.RunID, &e.Eval, &e.Name, &e.Status, &e.Answer, &problems, &passed, &datasetID, &e.Error, &e.Steps,
		&e.CostUSD, &e.StartedAt, &finished); err != nil {
		return nil, err
	}
	if datasetID.Valid {
		e.DatasetID = &datasetID.Int64
	}
	e.Problems = parseList(problems)
	e.PassedChecks = parseList(passed)
	e.StartedAt = e.StartedAt.UTC()
	if finished.Valid {
		e.FinishedAt = finished.Time.UTC()
	}
	e.Trajectories = []model.ExperimentTrajectory{}
	return &e, nil
}

// GetExperimentRun returns the run with all its cases and their trajectory references, or model.ErrNotFound.
func GetExperimentRun(ctx context.Context, d *sql.DB, runID int64) (*model.ExperimentRun, error) {
	run, err := getRun(ctx, d, runID)
	if err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `
		SELECT `+caseResultColumns+`
		FROM gb_experiment_case_results e
		JOIN gb_knowledge_case_meta k ON k.case_result_id = e.id
		WHERE e.run_id = $1
		ORDER BY e.id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list experiment case results: %w", err)
	}
	defer rows.Close()
	run.CaseResults = []model.ExperimentCaseResult{}
	byID := map[int64]int{}
	for rows.Next() {
		e, err := scanCaseResult(rows)
		if err != nil {
			return nil, err
		}
		byID[e.ID] = len(run.CaseResults)
		run.CaseResults = append(run.CaseResults, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	trows, err := d.QueryContext(ctx, `
		SELECT `+trajectoryColumns+`
		FROM gb_experiment_trajectories
		WHERE case_result_id IN (SELECT id FROM gb_experiment_case_results WHERE run_id = $1)
		ORDER BY case_result_id, seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("list experiment trajectories: %w", err)
	}
	defer trows.Close()
	for trows.Next() {
		t, err := scanTrajectory(trows)
		if err != nil {
			return nil, err
		}
		i := byID[t.CaseResultID]
		run.CaseResults[i].Trajectories = append(run.CaseResults[i].Trajectories, *t)
	}
	return run, trows.Err()
}

// GetExperimentCaseResultDataset returns the dataset (deleted or not) the case ran in, or model.ErrNotFound if the case
// does not exist or has none.
func GetExperimentCaseResultDataset(ctx context.Context, d *sql.DB, caseResultID int64) (*model.Dataset, error) {
	ds, err := scanDataset(d.QueryRowContext(ctx, datasetSelect+`
		WHERE id = (SELECT dataset_id FROM gb_knowledge_case_meta WHERE case_result_id = $1)`, caseResultID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return ds, err
}

// FindExperimentTrajectory returns the reference to the trajectory with the given ATIF trajectory_id, or
// model.ErrNotFound. Its CaseResultID leads to the case, and through it to the run and experiment.
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

// ListExperimentCaseHistory returns the results of one case over the runs of the experiment, oldest run first. Each
// result has its RunID; the trajectories are not loaded.
func ListExperimentCaseHistory(ctx context.Context, d *sql.DB, experimentID int64, eval, name string) ([]model.ExperimentCaseResult, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+caseResultColumns+`
		FROM gb_experiment_case_results e
		JOIN gb_knowledge_case_meta k ON k.case_result_id = e.id
		JOIN gb_experiment_runs r ON r.id = e.run_id
		WHERE r.experiment_id = $1 AND r.type = $2 AND e.eval = $3 AND e.case_name = $4
		ORDER BY r.id, e.id`, experimentID, model.EvalTypeKnowledge, eval, name)
	if err != nil {
		return nil, fmt.Errorf("list experiment case history: %w", err)
	}
	defer rows.Close()
	out := []model.ExperimentCaseResult{}
	for rows.Next() {
		e, err := scanCaseResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}
