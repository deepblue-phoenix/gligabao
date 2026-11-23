package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/sqlite"
)

//---------------------------------------------------------------------------------------------
// Trainings outside the sidecar (the gb_model_train_jobs table, see sqlite/sql/schema.sql)

// ClaimModelTrainJob makes the running row of j, the claim of the user's training: a running job of the user and model type whose
// last beat is older than staleBefore is marked lost first, and ErrModelTrainJobRunning is returned if one that is alive remains.
func ClaimModelTrainJob(ctx context.Context,
	d *sql.DB,
	j ModelTrainJob,
	staleBefore time.Time) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertModelTrainJob(ctx,
			d,
			j,
			staleBefore)
	case config.DBBackendPostgres:
		return postgres.InsertModelTrainJob(ctx,
			d,
			j,
			staleBefore)
	}
	return 0, unsupported()
}

// BeatModelTrainJob records that the instance still follows the running job, with its latest state; false if the job is no longer
// running on that instance.
func BeatModelTrainJob(ctx context.Context,
	d *sql.DB,
	jobID,
	instanceID,
	jobJSON string) (bool, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.BeatModelTrainJob(ctx,
			d,
			jobID,
			instanceID,
			jobJSON)
	case config.DBBackendPostgres:
		return postgres.BeatModelTrainJob(ctx,
			d,
			jobID,
			instanceID,
			jobJSON)
	}
	return false, unsupported()
}

// EndModelTrainJob ends a running (or lost) job with the status, error, last state and the model it made.
func EndModelTrainJob(ctx context.Context,
	d *sql.DB,
	jobID,
	status,
	errText,
	jobJSON string,
	modelID *int64) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.EndModelTrainJob(ctx,
			d,
			jobID,
			status,
			errText,
			jobJSON,
			modelID)
	case config.DBBackendPostgres:
		return postgres.EndModelTrainJob(ctx,
			d,
			jobID,
			status,
			errText,
			jobJSON,
			modelID)
	}
	return unsupported()
}

// ExpireModelTrainJobs marks the running jobs of the user that have not beaten since staleBefore as lost; it returns how many.
func ExpireModelTrainJobs(ctx context.Context,
	d *sql.DB,
	modelType,
	userID string,
	staleBefore time.Time) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ExpireModelTrainJobs(ctx,
			d,
			modelType,
			userID,
			staleBefore)
	case config.DBBackendPostgres:
		return postgres.ExpireModelTrainJobs(ctx,
			d,
			modelType,
			userID,
			staleBefore)
	}
	return 0, unsupported()
}

// LatestModelTrainJob returns the newest training of the user of that model type, or ErrNotFound.
func LatestModelTrainJob(ctx context.Context,
	d *sql.DB,
	modelType,
	userID string) (*ModelTrainJob, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.LatestModelTrainJob(ctx,
			d,
			modelType,
			userID)
	case config.DBBackendPostgres:
		return postgres.LatestModelTrainJob(ctx,
			d,
			modelType,
			userID)
	}
	return nil, unsupported()
}
