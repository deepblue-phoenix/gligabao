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
// gb_experiment_results) with their metadata (gb_training_eval_meta) and items (gb_training_eval_items), see sqlite/db_schema.go.
// An eval is one run with one eval in it; its ID is the ID of the gb_experiment_results row.

// RecordTrainedModelEval stores a new eval (status "running" unless e.Status says otherwise) and returns its ID. e.ID,
// e.CreatedAt and e.FinishedAt are ignored.
func RecordTrainedModelEval(ctx context.Context, d *sql.DB, e TrainedModelEval) (int64, error) {
	if e.Status == "" {
		e.Status = TrainedModelEvalRunning
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertTrainedModelEval(ctx, d, e)
	case config.DBBackendPostgres:
		return postgres.InsertTrainedModelEval(ctx, d, e)
	}
	return 0, unsupported()
}

// SaveTrainedModelEvalBatch stores the items of a finished judge batch and the eval's counters after it (e.ID names the
// eval; its JudgeModel, counters and the judge's usage are written), atomically. ErrNotFound if there is no such eval.
func SaveTrainedModelEvalBatch(ctx context.Context, d *sql.DB, e TrainedModelEval, items []TrainedModelEvalItem) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.SaveTrainedModelEvalBatch(ctx, d, e, items)
	case config.DBBackendPostgres:
		return postgres.SaveTrainedModelEvalBatch(ctx, d, e, items)
	}
	return unsupported()
}

// FinishTrainedModelEval ends a running eval: status is TrainedModelEvalCompleted, Failed (with the error text) or Cancelled.
// An eval that is not running any more is left as it is. ErrNotFound if there is no such eval.
func FinishTrainedModelEval(ctx context.Context, d *sql.DB, id int64, status, errText string) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FinishTrainedModelEval(ctx, d, id, status, errText)
	case config.DBBackendPostgres:
		return postgres.FinishTrainedModelEval(ctx, d, id, status, errText)
	}
	return unsupported()
}

// FailRunningTrainedModelEvals sets every eval that is still running to failed with the reason (the ones a restart of the
// server interrupted) and returns how many there were.
func FailRunningTrainedModelEvals(ctx context.Context, d *sql.DB, reason string) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FailRunningTrainedModelEvals(ctx, d, reason)
	case config.DBBackendPostgres:
		return postgres.FailRunningTrainedModelEvals(ctx, d, reason)
	}
	return 0, unsupported()
}

// GetTrainedModelEval returns the eval with that ID, or ErrNotFound.
func GetTrainedModelEval(ctx context.Context, d *sql.DB, id int64) (*TrainedModelEval, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainedModelEval(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetTrainedModelEval(ctx, d, id)
	}
	return nil, unsupported()
}

// ListTrainedModelEvals returns the newest evals first, at most limit of them (all for limit <= 0): those of trained model
// modelID, or of every model when modelID is 0.
func ListTrainedModelEvals(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]TrainedModelEval, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModelEvals(ctx, d, modelID, limit)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModelEvals(ctx, d, modelID, limit)
	}
	return nil, unsupported()
}

// ListTrainedModelEvalItems returns the items of an eval stored so far (one per example whose judge batch is done), in dataset order.
func ListTrainedModelEvalItems(ctx context.Context, d *sql.DB, evalID int64) ([]TrainedModelEvalItem, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModelEvalItems(ctx, d, evalID)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModelEvalItems(ctx, d, evalID)
	}
	return nil, unsupported()
}
