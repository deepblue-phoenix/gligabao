package relational

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------
// Training data audits of trained models: the model is asked the questions of its own fine-tuning dataset and an LLM judges the
// answers (gb_training_audit_runs and gb_training_audit_cases, see sqlite/db_schema.go). Separate from the evals of trained models
// (db_trained_model_eval.go), whose cases are meant to be ones the model never saw.

// RecordTrainedModelAuditRun stores a new audit run (status "running" unless a.Status says otherwise) and returns its ID. a.ID,
// a.CreatedAt and a.FinishedAt are ignored.
func RecordTrainedModelAuditRun(ctx context.Context, d *sql.DB, a TrainedModelAuditRun) (int64, error) {
	if a.Status == "" {
		a.Status = TrainedModelRunRunning
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertTrainedModelAuditRun(ctx, d, a)
	case config.DBBackendPostgres:
		return postgres.InsertTrainedModelAuditRun(ctx, d, a)
	}
	return 0, unsupported()
}

// SaveTrainedModelAuditRunBatch stores the cases of a finished judge batch and the audit run's counters after it (a.ID names the
// audit run; its JudgeModel, counters and the judge's usage are written), atomically. ErrNotFound if there is no such audit run.
func SaveTrainedModelAuditRunBatch(ctx context.Context, d *sql.DB, a TrainedModelAuditRun, cases []TrainedModelAuditCase) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.SaveTrainedModelAuditRunBatch(ctx, d, a, cases)
	case config.DBBackendPostgres:
		return postgres.SaveTrainedModelAuditRunBatch(ctx, d, a, cases)
	}
	return unsupported()
}

// FinishTrainedModelAuditRun ends a running audit run: status is TrainedModelRunCompleted, TrainedModelRunErrored (with the error text) or
// Cancelled. An audit run that is not running any more is left as it is. ErrNotFound if there is no such audit run.
func FinishTrainedModelAuditRun(ctx context.Context, d *sql.DB, id int64, status, errText string) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FinishTrainedModelAuditRun(ctx, d, id, status, errText)
	case config.DBBackendPostgres:
		return postgres.FinishTrainedModelAuditRun(ctx, d, id, status, errText)
	}
	return unsupported()
}

// FailRunningTrainedModelAuditRuns sets every audit run that is still running to error with the reason (the ones a restart of the
// server interrupted) and returns how many there were.
func FailRunningTrainedModelAuditRuns(ctx context.Context, d *sql.DB, reason string) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FailRunningTrainedModelAuditRuns(ctx, d, reason)
	case config.DBBackendPostgres:
		return postgres.FailRunningTrainedModelAuditRuns(ctx, d, reason)
	}
	return 0, unsupported()
}

// GetTrainedModelAuditRun returns the audit run with that ID, or ErrNotFound.
func GetTrainedModelAuditRun(ctx context.Context, d *sql.DB, id int64) (*TrainedModelAuditRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainedModelAuditRun(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetTrainedModelAuditRun(ctx, d, id)
	}
	return nil, unsupported()
}

// ListTrainedModelAuditRuns returns the newest audit runs first, at most limit of them (all for limit <= 0): those of trained model
// modelID, or of every model when modelID is 0.
func ListTrainedModelAuditRuns(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]TrainedModelAuditRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModelAuditRuns(ctx, d, modelID, limit)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModelAuditRuns(ctx, d, modelID, limit)
	}
	return nil, unsupported()
}

// ListTrainedModelAuditCases returns the cases of an audit run stored so far (one per example whose judge batch is done), in dataset order.
func ListTrainedModelAuditCases(ctx context.Context, d *sql.DB, auditRunID int64) ([]TrainedModelAuditCase, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModelAuditCases(ctx, d, auditRunID)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModelAuditCases(ctx, d, auditRunID)
	}
	return nil, unsupported()
}
