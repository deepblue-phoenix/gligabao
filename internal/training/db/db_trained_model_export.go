package db

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/sqlite"
)

//---------------------------------------------------------------------------------------------
// Exports of trained models (the gb_trained_model_exports table, see sqlite/db_schema.go)

// RecordTrainedModelExport stores a new export (status "running" unless e.Status says otherwise) and returns its ID.
// e.ID, e.CreatedAt and the completion fields are ignored.
func RecordTrainedModelExport(ctx context.Context, d *sql.DB, e TrainedModelExport) (int64, error) {
	if e.Status == "" {
		e.Status = TrainedModelExportRunning
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.InsertTrainedModelExport(ctx, d, e)
	case config.DBBackendPostgres:
		return postgres.InsertTrainedModelExport(ctx, d, e)
	}
	return 0, unsupported()
}

// SetTrainedModelExportDir records the local directory the weights of export id were fetched to. ErrNotFound if there is no such export.
func SetTrainedModelExportDir(ctx context.Context, d *sql.DB, id int64, dir string) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.SetTrainedModelExportDir(ctx, d, id, dir)
	case config.DBBackendPostgres:
		return postgres.SetTrainedModelExportDir(ctx, d, id, dir)
	}
	return unsupported()
}

// FinishTrainedModelExport ends export id: status is TrainedModelExportCompleted (with the exporter's ref and details, a JSON object,
// "" for none) or TrainedModelExportFailed (with the error text), durationMS how long it took. ErrNotFound if there is no such export.
func FinishTrainedModelExport(ctx context.Context, d *sql.DB, id int64, status, ref, detailsJSON, errText string, durationMS int64) error {
	if detailsJSON == "" {
		detailsJSON = "{}"
	}
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.FinishTrainedModelExport(ctx, d, id, status, ref, detailsJSON, errText, durationMS)
	case config.DBBackendPostgres:
		return postgres.FinishTrainedModelExport(ctx, d, id, status, ref, detailsJSON, errText, durationMS)
	}
	return unsupported()
}

// GetTrainedModelExport returns the export with that ID, or ErrNotFound.
func GetTrainedModelExport(ctx context.Context, d *sql.DB, id int64) (*TrainedModelExport, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainedModelExport(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.GetTrainedModelExport(ctx, d, id)
	}
	return nil, unsupported()
}

// ListTrainedModelExports returns the newest exports first, at most limit of them (all for limit <= 0): those of trained
// model modelID, or of every model when modelID is 0.
func ListTrainedModelExports(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]TrainedModelExport, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainedModelExports(ctx, d, modelID, limit)
	case config.DBBackendPostgres:
		return postgres.ListTrainedModelExports(ctx, d, modelID, limit)
	}
	return nil, unsupported()
}
