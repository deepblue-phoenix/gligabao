package relational

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------
// LLM-as-a-judge evals of trained models: the "training" rows of the universal eval tables (gb_experiment_runs and
// gb_experiment_results) with their metadata (gb_training_suite_run_meta) and cases (gb_training_case_results), see sqlite/db_schema.go.
// A suite run is one run with one suite run in it; its ID is the ID of the gb_experiment_results row.

// RecordTrainedModelSuiteRun stores a new suite run (status "running" unless e.Status says otherwise) and returns its ID. e.ID,
// e.CreatedAt and e.FinishedAt are ignored.
func RecordTrainedModelSuiteRun(ctx context.Context, d *sql.DB, e TrainedModelSuiteRun) (int64, error) {
	if e.Status == "" {
		e.Status = TrainedModelSuiteRunRunning
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertTrainedModelSuiteRun(ctx, d, e)
	case config.DBBackendPostgres:
		return postgres.InsertTrainedModelSuiteRun(ctx, d, e)
	}
	return 0, unsupported()
}

// SaveTrainedModelSuiteRunBatch stores the cases of a finished judge batch and the suite run's counters after it (e.ID names the
// suite run; its JudgeModel, counters and the judge's usage are written), atomically. ErrNotFound if there is no such suite run.
func SaveTrainedModelSuiteRunBatch(ctx context.Context, d *sql.DB, e TrainedModelSuiteRun, cases []TrainedModelCaseResult) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.SaveTrainedModelSuiteRunBatch(ctx, d, e, cases)
	case config.DBBackendPostgres:
		return postgres.SaveTrainedModelSuiteRunBatch(ctx, d, e, cases)
	}
	return unsupported()
}

// FinishTrainedModelSuiteRun ends a running suite run: status is TrainedModelSuiteRunCompleted, Failed (with the error text) or Cancelled.
// A suite run that is not running any more is left as it is. ErrNotFound if there is no such suite run.
func FinishTrainedModelSuiteRun(ctx context.Context, d *sql.DB, id int64, status, errText string) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FinishTrainedModelSuiteRun(ctx, d, id, status, errText)
	case config.DBBackendPostgres:
		return postgres.FinishTrainedModelSuiteRun(ctx, d, id, status, errText)
	}
	return unsupported()
}

// FailRunningTrainedModelSuiteRuns sets every suite run that is still running to failed with the reason (the ones a restart of the
// server interrupted) and returns how many there were.
func FailRunningTrainedModelSuiteRuns(ctx context.Context, d *sql.DB, reason string) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FailRunningTrainedModelSuiteRuns(ctx, d, reason)
	case config.DBBackendPostgres:
		return postgres.FailRunningTrainedModelSuiteRuns(ctx, d, reason)
	}
	return 0, unsupported()
}

// GetTrainedModelSuiteRun returns the suite run with that ID, or ErrNotFound.
func GetTrainedModelSuiteRun(ctx context.Context, d *sql.DB, id int64) (*TrainedModelSuiteRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainedModelSuiteRun(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetTrainedModelSuiteRun(ctx, d, id)
	}
	return nil, unsupported()
}

// ListTrainedModelSuiteRuns returns the newest suite runs first, at most limit of them (all for limit <= 0): those of trained model
// modelID, or of every model when modelID is 0.
func ListTrainedModelSuiteRuns(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]TrainedModelSuiteRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModelSuiteRuns(ctx, d, modelID, limit)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModelSuiteRuns(ctx, d, modelID, limit)
	}
	return nil, unsupported()
}

// ListTrainedModelCaseResults returns the cases of a suite run stored so far (one per example whose judge batch is done), in dataset order.
func ListTrainedModelCaseResults(ctx context.Context, d *sql.DB, suiteRunID int64) ([]TrainedModelCaseResult, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModelCaseResults(ctx, d, suiteRunID)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModelCaseResults(ctx, d, suiteRunID)
	}
	return nil, unsupported()
}
