package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Training job runs (gb_training_job_runs, see db_schema.go)

// InsertTrainingJobRun stores one training job run and returns its ID. r.ID and r.CreatedAt are ignored.
func InsertTrainingJobRun(ctx context.Context, d *sql.DB, r model.TrainingJobRun) (int64, error) {
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_training_job_runs (job_id, status, model_size, num_train_steps, started_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.JobID, r.Status, r.ModelSize, r.NumTrainSteps, FormatTime(r.StartedAt), FormatTime(Now()))
	if err != nil {
		return 0, fmt.Errorf("insert training job run: %w", err)
	}
	return res.LastInsertId()
}

// GetTrainingJobRun returns the training job run with that ID, or model.ErrNotFound.
func GetTrainingJobRun(ctx context.Context, d *sql.DB, id int64) (*model.TrainingJobRun, error) {
	var r model.TrainingJobRun
	var started, created string
	err := d.QueryRowContext(ctx, `
		SELECT id, job_id, status, model_size, num_train_steps, started_at, created_at
		FROM gb_training_job_runs
		WHERE id = ?`, id).Scan(&r.ID, &r.JobID, &r.Status, &r.ModelSize, &r.NumTrainSteps, &started, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.StartedAt = parseStoredTime(started)
	r.CreatedAt = parseStoredTime(created)
	return &r, nil
}
