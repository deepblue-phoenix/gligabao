package relational

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

//---------------------------------------------------------------------------------------------
// Training datasets (the gb_training_datasets table, see sqlite/db_schema.go). The datasets themselves are the files of the training datasets directory (internal/api/training).

// SaveTrainingDataset records a dataset file (Filename, SizeBytes, NumRecords, SHA256 of ds are used) as
// TrainingDatasetStored, replacing the record with the same filename, and returns its ID.
func SaveTrainingDataset(ctx context.Context, d *sql.DB, ds TrainingDataset) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.UpsertTrainingDataset(ctx, d, ds)
	case config.DBBackendPostgres:
		return postgres.UpsertTrainingDataset(ctx, d, ds)
	}
	return 0, unsupported()
}

// MarkTrainingDatasetSynced records that the sidecar has the file of dataset id whose digest is sha256. It does nothing
// (and reports false) if the dataset has another digest by now, i.e. it was uploaded again. ErrNotFound if there is no
// such dataset.
func MarkTrainingDatasetSynced(ctx context.Context, d *sql.DB, id int64, sha256 string) (bool, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.MarkTrainingDatasetSynced(ctx, d, id, sha256)
	case config.DBBackendPostgres:
		return postgres.MarkTrainingDatasetSynced(ctx, d, id, sha256)
	}
	return false, unsupported()
}

// GetTrainingDatasetByFilename returns the dataset with that file name, or ErrNotFound.
func GetTrainingDatasetByFilename(ctx context.Context, d *sql.DB, filename string) (*TrainingDataset, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetTrainingDatasetByFilename(ctx, d, filename)
	case config.DBBackendPostgres:
		return postgres.GetTrainingDatasetByFilename(ctx, d, filename)
	}
	return nil, unsupported()
}

// ListTrainingDatasets returns the datasets, the most recently uploaded first.
func ListTrainingDatasets(ctx context.Context, d *sql.DB) ([]TrainingDataset, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListTrainingDatasets(ctx, d)
	case config.DBBackendPostgres:
		return postgres.ListTrainingDatasets(ctx, d)
	}
	return nil, unsupported()
}
