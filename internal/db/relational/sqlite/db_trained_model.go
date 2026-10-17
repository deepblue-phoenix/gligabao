package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Trained models (gb_trained_models, see db_schema.go)

// InsertTrainedModel stores the model a completed job saved and returns its ID and whether it is new: the job (JobID)
// may have a model already, which is then left as it is and its ID returned. m.ID and m.CreatedAt are ignored.
func InsertTrainedModel(ctx context.Context, d *sql.DB, m model.TrainedModel) (int64, bool, error) {
	var runID any
	if m.RunID != nil {
		runID = *m.RunID
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_trained_models (name, run_id, job_id, base_model, model_size, dataset, dataset_sha256, num_examples,
			num_train_steps, final_loss, params, path, files, size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (job_id) DO NOTHING`,
		m.Name, runID, m.JobID, m.BaseModel, m.ModelSize, m.Dataset, m.DatasetSHA256, m.NumExamples, m.NumTrainSteps,
		m.FinalLoss, m.ParamsJSON(), m.Path, m.FilesJSON(), m.SizeBytes, FormatTime(Now()))
	if err != nil {
		return 0, false, fmt.Errorf("insert trained model: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		id, err := res.LastInsertId()
		return id, true, err
	}
	var id int64
	err = d.QueryRowContext(ctx, `SELECT id FROM gb_trained_models WHERE job_id = ?`, m.JobID).Scan(&id)
	return id, false, err
}

const trainedModelColumns = `id, name, run_id, job_id, base_model, model_size, dataset, dataset_sha256, num_examples,
	num_train_steps, final_loss, params, path, files, size_bytes, created_at`

func scanTrainedModel(s rowScanner) (*model.TrainedModel, error) {
	var m model.TrainedModel
	var runID sql.NullInt64
	var finalLoss sql.NullFloat64
	var params, files, created string
	if err := s.Scan(&m.ID, &m.Name, &runID, &m.JobID, &m.BaseModel, &m.ModelSize, &m.Dataset, &m.DatasetSHA256, &m.NumExamples,
		&m.NumTrainSteps, &finalLoss, &params, &m.Path, &files, &m.SizeBytes, &created); err != nil {
		return nil, err
	}
	if runID.Valid {
		m.RunID = &runID.Int64
	}
	if finalLoss.Valid {
		m.FinalLoss = &finalLoss.Float64
	}
	m.SetStoredJSON(params, files)
	m.CreatedAt = parseStoredTime(created)
	return &m, nil
}

// GetTrainedModel returns the trained model with that ID, or model.ErrNotFound.
func GetTrainedModel(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModel, error) {
	m, err := scanTrainedModel(d.QueryRowContext(ctx, `SELECT `+trainedModelColumns+` FROM gb_trained_models WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return m, err
}

// ListTrainedModels returns the newest trained models first, at most limit of them (all for limit <= 0).
func ListTrainedModels(ctx context.Context, d *sql.DB, limit int) ([]model.TrainedModel, error) {
	q := `SELECT ` + trainedModelColumns + ` FROM gb_trained_models ORDER BY id DESC`
	var args []any
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list trained models: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModel{}
	for rows.Next() {
		m, err := scanTrainedModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}
