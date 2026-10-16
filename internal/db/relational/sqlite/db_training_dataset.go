package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Training datasets (gb_training_datasets, see db_schema.go)

// UpsertTrainingDataset records a dataset file under its filename (a new row, or replacing the row that has that name) as
// 'stored', not synced, and returns its ID. Only Filename, SizeBytes, NumRecords and SHA256 of ds are used.
func UpsertTrainingDataset(ctx context.Context, d *sql.DB, ds model.TrainingDataset) (int64, error) {
	now := FormatTime(Now())
	var id int64
	err := d.QueryRowContext(ctx, `
		INSERT INTO gb_training_datasets (filename, size_bytes, num_records, sha256, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(filename) DO UPDATE SET size_bytes = excluded.size_bytes, num_records = excluded.num_records,
			sha256 = excluded.sha256, status = excluded.status, synced_at = NULL, updated_at = excluded.updated_at
		RETURNING id`,
		ds.Filename, ds.SizeBytes, ds.NumRecords, ds.SHA256, model.TrainingDatasetStored, now, now).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert training dataset: %w", err)
	}
	return id, nil
}

// MarkTrainingDatasetSynced records that the sidecar has the file whose digest is sha256: it only sets the dataset 'synced'
// if it still has that digest (it may have been uploaded again meanwhile). It reports whether it did; model.ErrNotFound if
// there is no such dataset.
func MarkTrainingDatasetSynced(ctx context.Context, d *sql.DB, id int64, sha256 string) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE gb_training_datasets SET status = ?, synced_at = ? WHERE id = ? AND sha256 = ?`,
		model.TrainingDatasetSynced, FormatTime(Now()), id, sha256)
	if err != nil {
		return false, fmt.Errorf("mark training dataset synced: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, nil
	}
	var one int
	if err := d.QueryRowContext(ctx, `SELECT 1 FROM gb_training_datasets WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return false, model.ErrNotFound
	}
	return false, nil
}

const trainingDatasetColumns = `id, filename, size_bytes, num_records, sha256, status, created_at, updated_at, synced_at`

func scanTrainingDataset(s rowScanner) (*model.TrainingDataset, error) {
	var ds model.TrainingDataset
	var created, updated string
	var synced sql.NullString
	if err := s.Scan(&ds.ID, &ds.Filename, &ds.SizeBytes, &ds.NumRecords, &ds.SHA256, &ds.Status, &created, &updated, &synced); err != nil {
		return nil, err
	}
	ds.CreatedAt, ds.UpdatedAt = parseStoredTime(created), parseStoredTime(updated)
	if synced.Valid {
		t := parseStoredTime(synced.String)
		ds.SyncedAt = &t
	}
	return &ds, nil
}

// GetTrainingDatasetByFilename returns the dataset with that file name, or model.ErrNotFound.
func GetTrainingDatasetByFilename(ctx context.Context, d *sql.DB, filename string) (*model.TrainingDataset, error) {
	ds, err := scanTrainingDataset(d.QueryRowContext(ctx,
		`SELECT `+trainingDatasetColumns+` FROM gb_training_datasets WHERE filename = ?`, filename))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return ds, err
}

// ListTrainingDatasets returns the datasets, the most recently uploaded first.
func ListTrainingDatasets(ctx context.Context, d *sql.DB) ([]model.TrainingDataset, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+trainingDatasetColumns+` FROM gb_training_datasets ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list training datasets: %w", err)
	}
	defer rows.Close()
	out := []model.TrainingDataset{}
	for rows.Next() {
		ds, err := scanTrainingDataset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ds)
	}
	return out, rows.Err()
}
