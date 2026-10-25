package db

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/sqlite"
)

//---------------------------------------------------------------------------------------------
// Deployments of trained models (the gb_trained_model_deployments table, see sqlite/db_schema.go)

// RecordTrainedModelDeployment stores a new deployment (status "running" unless e.Status says otherwise) and returns its ID.
// e.ID, e.CreatedAt and the completion fields are ignored.
func RecordTrainedModelDeployment(ctx context.Context, d *sql.DB, e TrainedModelDeployment) (int64, error) {
	if e.Status == "" {
		e.Status = TrainedModelDeploymentRunning
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertTrainedModelDeployment(ctx, d, e)
	case config.DBBackendPostgres:
		return postgres.InsertTrainedModelDeployment(ctx, d, e)
	}
	return 0, unsupported()
}

// SetTrainedModelDeployDir records the local directory the weights of deploy id were fetched to. ErrNotFound if there is no such deploy.
func SetTrainedModelDeployDir(ctx context.Context, d *sql.DB, id int64, dir string) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.SetTrainedModelDeployDir(ctx, d, id, dir)
	case config.DBBackendPostgres:
		return postgres.SetTrainedModelDeployDir(ctx, d, id, dir)
	}
	return unsupported()
}

// FinishTrainedModelDeployment ends deploy id: status is TrainedModelDeploymentCompleted (with the deployer's ref and details, a JSON object,
// "" for none) or TrainedModelDeploymentFailed (with the error text), durationMS how long it took. ErrNotFound if there is no such deploy.
func FinishTrainedModelDeployment(ctx context.Context, d *sql.DB, id int64, status, ref, detailsJSON, errText string, durationMS int64) error {
	if detailsJSON == "" {
		detailsJSON = "{}"
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FinishTrainedModelDeployment(ctx, d, id, status, ref, detailsJSON, errText, durationMS)
	case config.DBBackendPostgres:
		return postgres.FinishTrainedModelDeployment(ctx, d, id, status, ref, detailsJSON, errText, durationMS)
	}
	return unsupported()
}

// GetTrainedModelDeployment returns the deployment with that ID, or ErrNotFound.
func GetTrainedModelDeployment(ctx context.Context, d *sql.DB, id int64) (*TrainedModelDeployment, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainedModelDeployment(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetTrainedModelDeployment(ctx, d, id)
	}
	return nil, unsupported()
}

// ListTrainedModelDeployments returns the newest deployments first, at most limit of them (all for limit <= 0): those of trained
// model modelID, or of every model when modelID is 0.
func ListTrainedModelDeployments(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]TrainedModelDeployment, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModelDeployments(ctx, d, modelID, limit)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModelDeployments(ctx, d, modelID, limit)
	}
	return nil, unsupported()
}
