package relational

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------
// Training job runs (the gb_training_job_runs table, see sqlite/db_schema.go)

// RecordTrainingJobRun stores a training job the sidecar started (one record per job) and returns its ID. r.ID and
// r.CreatedAt are ignored, and so are the fields that only events set (last step and loss, final loss, error, finish time).
func RecordTrainingJobRun(ctx context.Context, d *sql.DB, r TrainingJobRun) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertTrainingJobRun(ctx, d, r)
	case config.DBBackendPostgres:
		return postgres.InsertTrainingJobRun(ctx, d, r)
	}
	return 0, unsupported()
}

// GetTrainingJobRun returns the training job run with that ID, or ErrNotFound.
func GetTrainingJobRun(ctx context.Context, d *sql.DB, id int64) (*TrainingJobRun, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainingJobRun(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetTrainingJobRun(ctx, d, id)
	}
	return nil, unsupported()
}

// ListTrainingJobRuns returns the newest training job runs first, at most limit of them. With unfinished it returns only
// the runs that have not completed or failed, oldest first, and limit is ignored.
func ListTrainingJobRuns(ctx context.Context, d *sql.DB, limit int, unfinished bool) ([]TrainingJobRun, error) {
	if unfinished {
		limit = 0
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainingJobRuns(ctx, d, limit, unfinished)
	case config.DBBackendPostgres:
		return postgres.ListTrainingJobRuns(ctx, d, limit, unfinished)
	}
	return nil, unsupported()
}

// UpdateTrainingJobRun writes the fields of u that are set to a run, or returns ErrNotFound.
func UpdateTrainingJobRun(ctx context.Context, d *sql.DB, id int64, u TrainingJobRunUpdate) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.UpdateTrainingJobRun(ctx, d, id, u)
	case config.DBBackendPostgres:
		return postgres.UpdateTrainingJobRun(ctx, d, id, u)
	}
	return unsupported()
}

// RecordTrainingJobEvent stores the event with index seq (in the sidecar's event log for the job) of a run, as typ
// ("status" or "progress") and JSON data. It reports whether the event was new: a seq already stored is left as it is.
func RecordTrainingJobEvent(ctx context.Context, d *sql.DB, runID int64, seq int, typ, data string) (bool, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertTrainingJobEvent(ctx, d, runID, seq, typ, data)
	case config.DBBackendPostgres:
		return postgres.InsertTrainingJobEvent(ctx, d, runID, seq, typ, data)
	}
	return false, unsupported()
}

// ListTrainingJobEvents returns the events of a run with seq >= from, in order.
func ListTrainingJobEvents(ctx context.Context, d *sql.DB, runID int64, from int) ([]TrainingJobEvent, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainingJobEvents(ctx, d, runID, from)
	case config.DBBackendPostgres:
		return postgres.ListTrainingJobEvents(ctx, d, runID, from)
	}
	return nil, unsupported()
}

// NextTrainingJobEventSeq returns the seq the next event of a run would have: one past the newest stored (0 if none).
func NextTrainingJobEventSeq(ctx context.Context, d *sql.DB, runID int64) (int, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.NextTrainingJobEventSeq(ctx, d, runID)
	case config.DBBackendPostgres:
		return postgres.NextTrainingJobEventSeq(ctx, d, runID)
	}
	return 0, unsupported()
}
