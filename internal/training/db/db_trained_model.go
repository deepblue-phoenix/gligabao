package db

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/sqlite"
)

//---------------------------------------------------------------------------------------------
// Trained models (the gb_trained_models table, see sqlite/db_schema.go)

// RecordTrainedModel stores the model a completed training job saved, and returns its ID and whether it is new: a model
// of a job (JobID) that already has one is left as it is (the job's completed event was replayed) and its ID is returned.
// m.ID and m.CreatedAt are ignored; an empty Name becomes one made from the settings (DefaultTrainedModelName).
func RecordTrainedModel(ctx context.Context, d *sql.DB, m TrainedModel) (int64, bool, error) {
	if m.Name == "" {
		m.Name = DefaultTrainedModelName(m)
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertTrainedModel(ctx, d, m)
	case config.DBBackendPostgres:
		return postgres.InsertTrainedModel(ctx, d, m)
	}
	return 0, false, unsupported()
}

// GetTrainedModel returns the trained model with that ID, or ErrNotFound.
func GetTrainedModel(ctx context.Context, d *sql.DB, id int64) (*TrainedModel, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainedModel(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetTrainedModel(ctx, d, id)
	}
	return nil, unsupported()
}

// ListTrainedModels returns the newest trained models first, at most limit of them (all for limit <= 0).
func ListTrainedModels(ctx context.Context, d *sql.DB, limit int) ([]TrainedModel, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModels(ctx, d, limit)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModels(ctx, d, limit)
	}
	return nil, unsupported()
}
