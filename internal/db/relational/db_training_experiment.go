package relational

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------
// Training experiments: named series of evals of trained models, the "training" rows of gb_experiments and gb_experiment_runs (see
// sqlite/db_schema.go). A run is an eval (RecordTrainedModelEval) that was made part of an experiment; it holds no results of
// its own, the eval does.

// EnsureTrainingExperiment returns the training experiment called name, creating it first (with the given description and
// settings) if needed. An existing experiment is returned unchanged.
func EnsureTrainingExperiment(ctx context.Context, d *sql.DB, name, description string, cfg map[string]any) (*TrainingExperiment, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.EnsureTrainingExperiment(ctx, d, name, description, cfg)
	case config.DBBackendPostgres:
		return postgres.EnsureTrainingExperiment(ctx, d, name, description, cfg)
	}
	return nil, unsupported()
}

// GetTrainingExperiment returns the training experiment with that ID (with its number of runs), or ErrNotFound.
func GetTrainingExperiment(ctx context.Context, d *sql.DB, id int64) (*TrainingExperiment, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainingExperiment(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetTrainingExperiment(ctx, d, id)
	}
	return nil, unsupported()
}

// ListTrainingExperiments returns all training experiments, newest first.
func ListTrainingExperiments(ctx context.Context, d *sql.DB) ([]TrainingExperiment, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainingExperiments(ctx, d)
	case config.DBBackendPostgres:
		return postgres.ListTrainingExperiments(ctx, d)
	}
	return nil, unsupported()
}

// StartTrainingExperimentRun makes the eval a run of the experiment and returns the run. It returns ErrNotFound if the
// experiment or the eval does not exist, and an error if the eval is a run already (an eval is in one experiment only).
func StartTrainingExperimentRun(ctx context.Context, d *sql.DB, experimentID, evalID int64) (*TrainingExperimentRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.StartTrainingExperimentRun(ctx, d, experimentID, evalID)
	case config.DBBackendPostgres:
		return postgres.StartTrainingExperimentRun(ctx, d, experimentID, evalID)
	}
	return nil, unsupported()
}

// GetTrainingExperimentRun returns the run with that ID, with its eval as it is now and its model, or ErrNotFound.
func GetTrainingExperimentRun(ctx context.Context, d *sql.DB, id int64) (*TrainingExperimentRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainingExperimentRun(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetTrainingExperimentRun(ctx, d, id)
	}
	return nil, unsupported()
}

// ListTrainingExperimentRuns returns the runs of the experiment, oldest first, each with its eval as it is now and its model.
func ListTrainingExperimentRuns(ctx context.Context, d *sql.DB, experimentID int64) ([]TrainingExperimentRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainingExperimentRuns(ctx, d, experimentID)
	case config.DBBackendPostgres:
		return postgres.ListTrainingExperimentRuns(ctx, d, experimentID)
	}
	return nil, unsupported()
}
