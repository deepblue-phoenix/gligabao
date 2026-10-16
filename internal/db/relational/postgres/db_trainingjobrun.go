package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Training job runs (see sqlite/db_trainingjobrun.go for what the functions do)

// InsertTrainingJobRun stores one training job run and returns its ID.
func InsertTrainingJobRun(ctx context.Context, d *sql.DB, r model.TrainingJobRun) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx, `
		INSERT INTO gb_training_job_runs (job_id, status, model_size, num_train_steps, started_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`,
		r.JobID, r.Status, r.ModelSize, r.NumTrainSteps, Normalize(r.StartedAt), Now()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert training job run: %w", err)
	}
	return id, nil
}

// GetTrainingJobRun returns the training job run with that ID, or model.ErrNotFound.
func GetTrainingJobRun(ctx context.Context, d *sql.DB, id int64) (*model.TrainingJobRun, error) {
	var r model.TrainingJobRun
	err := d.QueryRowContext(ctx, `
		SELECT id, job_id, status, model_size, num_train_steps, started_at, created_at
		FROM gb_training_job_runs
		WHERE id = $1`, id).Scan(&r.ID, &r.JobID, &r.Status, &r.ModelSize, &r.NumTrainSteps, &r.StartedAt, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.StartedAt, r.CreatedAt = r.StartedAt.UTC(), r.CreatedAt.UTC()
	return &r, nil
}
