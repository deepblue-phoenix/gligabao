package relational

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------
// Eval experiments (see package experiments for the high-level API; the tables are described in
// sqlite/db_schema.go)

// EnsureExperiment returns the experiment called name, creating it first (with the given description and settings)
// if needed. An existing experiment is returned unchanged.
func EnsureExperiment(ctx context.Context, d *sql.DB, name, description string, cfg map[string]any) (*Experiment, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.EnsureExperiment(ctx, d, name, description, cfg)
	case config.DBBackendPostgres:
		return postgres.EnsureExperiment(ctx, d, name, description, cfg)
	}
	return nil, unsupported()
}

// GetExperiment returns the experiment called name, with its Evals, or ErrNotFound.
func GetExperiment(ctx context.Context, d *sql.DB, name string) (*Experiment, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetExperiment(ctx, d, name)
	case config.DBBackendPostgres:
		return postgres.GetExperiment(ctx, d, name)
	}
	return nil, unsupported()
}

// ListExperiments returns all experiments, oldest first, without their Evals.
func ListExperiments(ctx context.Context, d *sql.DB) ([]Experiment, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListExperiments(ctx, d)
	case config.DBBackendPostgres:
		return postgres.ListExperiments(ctx, d)
	}
	return nil, unsupported()
}

// StartExperimentRun starts a run of the experiment (status running). params describes how the run was invoked.
// It returns ErrNotFound if the experiment does not exist.
func StartExperimentRun(ctx context.Context, d *sql.DB, experimentID int64, params map[string]any) (*ExperimentRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.StartExperimentRun(ctx, d, experimentID, params)
	case config.DBBackendPostgres:
		return postgres.StartExperimentRun(ctx, d, experimentID, params)
	}
	return nil, unsupported()
}

// RecordExperimentResult stores the result of one eval of a running run (e.RunID) together with the references to
// its trajectories (numbered in order), and adds the eval to the experiment's evals. It returns the stored copy,
// ErrNotFound if the run does not exist, and ErrRunNotRunning if it is already finished.
func RecordExperimentResult(ctx context.Context, d *sql.DB, e ExperimentResult) (*ExperimentResult, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.RecordExperimentResult(ctx, d, e)
	case config.DBBackendPostgres:
		return postgres.RecordExperimentResult(ctx, d, e)
	}
	return nil, unsupported()
}

// FinishExperimentRun totals the evals recorded for the run and ends it: passed if every eval passed, otherwise
// failed. Finishing a finished run changes nothing. It returns the stored run (without Evals), or ErrNotFound.
func FinishExperimentRun(ctx context.Context, d *sql.DB, runID int64) (*ExperimentRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FinishExperimentRun(ctx, d, runID)
	case config.DBBackendPostgres:
		return postgres.FinishExperimentRun(ctx, d, runID)
	}
	return nil, unsupported()
}

// ListExperimentRuns returns the runs of the experiment, oldest first, without their Evals.
func ListExperimentRuns(ctx context.Context, d *sql.DB, experimentID int64) ([]ExperimentRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListExperimentRuns(ctx, d, experimentID)
	case config.DBBackendPostgres:
		return postgres.ListExperimentRuns(ctx, d, experimentID)
	}
	return nil, unsupported()
}

// GetExperimentRun returns the run with all its evals and their trajectory references, or ErrNotFound.
func GetExperimentRun(ctx context.Context, d *sql.DB, runID int64) (*ExperimentRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetExperimentRun(ctx, d, runID)
	case config.DBBackendPostgres:
		return postgres.GetExperimentRun(ctx, d, runID)
	}
	return nil, unsupported()
}

// GetExperimentResultDataset returns the dataset (deleted or not) an eval ran in, whose graph is the one the eval
// built, or ErrNotFound if the eval does not exist or has none.
func GetExperimentResultDataset(ctx context.Context, d *sql.DB, resultID int64) (*Dataset, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetExperimentResultDataset(ctx, d, resultID)
	case config.DBBackendPostgres:
		return postgres.GetExperimentResultDataset(ctx, d, resultID)
	}
	return nil, unsupported()
}

// FindExperimentTrajectory returns the reference to the trajectory with the given ATIF trajectory_id, or ErrNotFound.
// Its ResultID leads to the eval, and through it to the run and the experiment.
func FindExperimentTrajectory(ctx context.Context, d *sql.DB, trajectoryID string) (*ExperimentTrajectory, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FindExperimentTrajectory(ctx, d, trajectoryID)
	case config.DBBackendPostgres:
		return postgres.FindExperimentTrajectory(ctx, d, trajectoryID)
	}
	return nil, unsupported()
}

// ListExperimentEvalHistory returns the results of one eval (suite and case name) over the runs of the experiment,
// oldest run first. It is empty if the eval was never recorded.
func ListExperimentEvalHistory(ctx context.Context, d *sql.DB, experimentID int64, suite, name string) ([]ExperimentEvalPoint, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListExperimentEvalHistory(ctx, d, experimentID, suite, name)
	case config.DBBackendPostgres:
		return postgres.ListExperimentEvalHistory(ctx, d, experimentID, suite, name)
	}
	return nil, unsupported()
}
