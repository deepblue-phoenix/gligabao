package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Inference requests (see sqlite/db_inference.go for what the functions do)

// InsertTrainedModelInference stores a new request and returns its ID.
func InsertTrainedModelInference(ctx context.Context, d *sql.DB, r model.TrainedModelInference) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx, `
		INSERT INTO gb_trained_model_inferences (trained_model_id, prompt, status, model_size, model_path, max_tokens, temperature, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		r.TrainedModelID, r.Prompt, r.Status, r.ModelSize, r.ModelPath, r.MaxTokens, r.Temperature, Now()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert inference request: %w", err)
	}
	return id, nil
}

// FinishTrainedModelInference ends a request.
func FinishTrainedModelInference(ctx context.Context, d *sql.DB, id int64, status, response, errText string, inputTokens, outputTokens int, coldStart *bool, loadSeconds float64, durationMS int64) error {
	res, err := d.ExecContext(ctx, `
		UPDATE gb_trained_model_inferences SET status = $1, response = $2, error = $3, input_tokens = $4, output_tokens = $5, cold_start = $6, load_seconds = $7, duration_ms = $8, completed_at = $9 WHERE id = $10`,
		status, response, errText, inputTokens, outputTokens, coldStart, loadSeconds, durationMS, Now(), id)
	if err != nil {
		return fmt.Errorf("finish inference request: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

const trainedModelInferenceColumns = `id, trained_model_id, prompt, response, status, error, model_size, model_path, max_tokens,
	temperature, input_tokens, output_tokens, cold_start, load_seconds, duration_ms, created_at, completed_at`

func scanTrainedModelInference(s rowScanner) (*model.TrainedModelInference, error) {
	var r model.TrainedModelInference
	var temp sql.NullFloat64
	var cold sql.NullBool
	var completed sql.NullTime
	if err := s.Scan(&r.ID, &r.TrainedModelID, &r.Prompt, &r.Response, &r.Status, &r.Error, &r.ModelSize, &r.ModelPath,
		&r.MaxTokens, &temp, &r.InputTokens, &r.OutputTokens, &cold, &r.LoadSeconds, &r.DurationMS, &r.CreatedAt, &completed); err != nil {
		return nil, err
	}
	if temp.Valid {
		r.Temperature = &temp.Float64
	}
	if cold.Valid {
		r.ColdStart = &cold.Bool
	}
	r.CreatedAt = r.CreatedAt.UTC()
	if completed.Valid {
		t := completed.Time.UTC()
		r.CompletedAt = &t
	}
	return &r, nil
}

// GetTrainedModelInference returns the request with that ID, or model.ErrNotFound.
func GetTrainedModelInference(ctx context.Context, d *sql.DB, id int64) (*model.TrainedModelInference, error) {
	r, err := scanTrainedModelInference(d.QueryRowContext(ctx, `SELECT `+trainedModelInferenceColumns+` FROM gb_trained_model_inferences WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return r, err
}

// ListTrainedModelInferences returns the newest requests first (of one trained model, or of all for modelID 0).
func ListTrainedModelInferences(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]model.TrainedModelInference, error) {
	q := `SELECT ` + trainedModelInferenceColumns + ` FROM gb_trained_model_inferences`
	var args []any
	if modelID > 0 {
		args = append(args, modelID)
		q += fmt.Sprintf(` WHERE trained_model_id = $%d`, len(args))
	}
	q += ` ORDER BY id DESC`
	if limit > 0 {
		args = append(args, limit)
		q += fmt.Sprintf(` LIMIT $%d`, len(args))
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list inference requests: %w", err)
	}
	defer rows.Close()
	out := []model.TrainedModelInference{}
	for rows.Next() {
		r, err := scanTrainedModelInference(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}
