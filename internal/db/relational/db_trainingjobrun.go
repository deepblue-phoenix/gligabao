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
// r.CreatedAt are ignored.
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
