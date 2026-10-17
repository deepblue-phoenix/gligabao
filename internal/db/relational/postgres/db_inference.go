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

// InsertInferenceRequest stores a new request and returns its ID.
func InsertInferenceRequest(ctx context.Context, d *sql.DB, r model.InferenceRequest) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx, `
		INSERT INTO gb_inference_requests (trained_model_id, prompt, status, model_size, model_path, max_tokens, temperature, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		r.TrainedModelID, r.Prompt, r.Status, r.ModelSize, r.ModelPath, r.MaxTokens, r.Temperature, Now()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert inference request: %w", err)
	}
	return id, nil
}

// FinishInferenceRequest ends a request.
func FinishInferenceRequest(ctx context.Context, d *sql.DB, id int64, status, response, errText string, durationMS int64) error {
	res, err := d.ExecContext(ctx, `
		UPDATE gb_inference_requests SET status = $1, response = $2, error = $3, duration_ms = $4, completed_at = $5 WHERE id = $6`,
		status, response, errText, durationMS, Now(), id)
	if err != nil {
		return fmt.Errorf("finish inference request: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFound
	}
	return nil
}

const inferenceRequestColumns = `id, trained_model_id, prompt, response, status, error, model_size, model_path, max_tokens,
	temperature, duration_ms, created_at, completed_at`

func scanInferenceRequest(s rowScanner) (*model.InferenceRequest, error) {
	var r model.InferenceRequest
	var temp sql.NullFloat64
	var completed sql.NullTime
	if err := s.Scan(&r.ID, &r.TrainedModelID, &r.Prompt, &r.Response, &r.Status, &r.Error, &r.ModelSize, &r.ModelPath,
		&r.MaxTokens, &temp, &r.DurationMS, &r.CreatedAt, &completed); err != nil {
		return nil, err
	}
	if temp.Valid {
		r.Temperature = &temp.Float64
	}
	r.CreatedAt = r.CreatedAt.UTC()
	if completed.Valid {
		t := completed.Time.UTC()
		r.CompletedAt = &t
	}
	return &r, nil
}

// GetInferenceRequest returns the request with that ID, or model.ErrNotFound.
func GetInferenceRequest(ctx context.Context, d *sql.DB, id int64) (*model.InferenceRequest, error) {
	r, err := scanInferenceRequest(d.QueryRowContext(ctx, `SELECT `+inferenceRequestColumns+` FROM gb_inference_requests WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return r, err
}

// ListInferenceRequests returns the newest requests first (of one trained model, or of all for modelID 0).
func ListInferenceRequests(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]model.InferenceRequest, error) {
	q := `SELECT ` + inferenceRequestColumns + ` FROM gb_inference_requests`
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
	out := []model.InferenceRequest{}
	for rows.Next() {
		r, err := scanInferenceRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}
