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
// gb_experiment_case_results) with their metadata (gb_training_eval_run_meta, gb_training_case_meta), see sqlite/db_schema.go.
// A training run is an eval run (its ID is that of the gb_experiment_runs row); each example is one of its case results.

// RecordTrainedModelEvalRun stores a new eval run (status "running" unless e.Status says otherwise) and returns its ID. e.ID,
// e.CreatedAt and e.FinishedAt are ignored.
func RecordTrainedModelEvalRun(ctx context.Context, d *sql.DB, e TrainedModelEvalRun) (int64, error) {
	if e.Status == "" {
		e.Status = TrainedModelEvalRunRunning
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertTrainedModelEvalRun(ctx, d, e)
	case config.DBBackendPostgres:
		return postgres.InsertTrainedModelEvalRun(ctx, d, e)
	}
	return 0, unsupported()
}

// SaveTrainedModelEvalRunBatch stores the cases of a finished judge batch and the eval run's counters after it (e.ID names the
// eval run; its JudgeModel, counters and the judge's usage are written), atomically. ErrNotFound if there is no such eval run.
func SaveTrainedModelEvalRunBatch(ctx context.Context, d *sql.DB, e TrainedModelEvalRun, cases []TrainedModelCaseResult) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.SaveTrainedModelEvalRunBatch(ctx, d, e, cases)
	case config.DBBackendPostgres:
		return postgres.SaveTrainedModelEvalRunBatch(ctx, d, e, cases)
	}
	return unsupported()
}

// FinishTrainedModelEvalRun ends a running eval run: status is TrainedModelEvalRunPassed, Failed (with the error text) or Cancelled.
// An eval run that is not running any more is left as it is. ErrNotFound if there is no such eval run.
func FinishTrainedModelEvalRun(ctx context.Context, d *sql.DB, id int64, status, errText string) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FinishTrainedModelEvalRun(ctx, d, id, status, errText)
	case config.DBBackendPostgres:
		return postgres.FinishTrainedModelEvalRun(ctx, d, id, status, errText)
	}
	return unsupported()
}

// FailRunningTrainedModelEvalRuns sets every eval run that is still running to failed with the reason (the ones a restart of the
// server interrupted) and returns how many there were.
func FailRunningTrainedModelEvalRuns(ctx context.Context, d *sql.DB, reason string) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FailRunningTrainedModelEvalRuns(ctx, d, reason)
	case config.DBBackendPostgres:
		return postgres.FailRunningTrainedModelEvalRuns(ctx, d, reason)
	}
	return 0, unsupported()
}

// GetTrainedModelEvalRun returns the eval run with that ID, or ErrNotFound.
func GetTrainedModelEvalRun(ctx context.Context, d *sql.DB, id int64) (*TrainedModelEvalRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainedModelEvalRun(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetTrainedModelEvalRun(ctx, d, id)
	}
	return nil, unsupported()
}

// ListTrainedModelEvalRuns returns the newest eval runs first, at most limit of them (all for limit <= 0): those of trained model
// modelID, or of every model when modelID is 0.
func ListTrainedModelEvalRuns(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]TrainedModelEvalRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModelEvalRuns(ctx, d, modelID, limit)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModelEvalRuns(ctx, d, modelID, limit)
	}
	return nil, unsupported()
}

// ListTrainedModelCaseResults returns the cases of an eval run stored so far (one per example whose judge batch is done), in dataset order.
func ListTrainedModelCaseResults(ctx context.Context, d *sql.DB, evalRunID int64) ([]TrainedModelCaseResult, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModelCaseResults(ctx, d, evalRunID)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModelCaseResults(ctx, d, evalRunID)
	}
	return nil, unsupported()
}
