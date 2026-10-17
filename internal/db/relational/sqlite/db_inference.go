package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

//---------------------------------------------------------------------------------------------
// Inference requests (gb_inference_requests, see db_schema.go)

// InsertInferenceRequest stores a new request and returns its ID.
func InsertInferenceRequest(ctx context.Context, d *sql.DB, r model.InferenceRequest) (int64, error) {
	res, err := d.ExecContext(ctx, `
		INSERT INTO gb_inference_requests (trained_model_id, prompt, status, model_size, model_path, max_tokens, temperature, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TrainedModelID, r.Prompt, r.Status, r.ModelSize, r.ModelPath, r.MaxTokens, r.Temperature, FormatTime(Now()))
	if err != nil {
		return 0, fmt.Errorf("insert inference request: %w", err)
	}
	return res.LastInsertId()
}

// FinishInferenceRequest ends a request (see relational.FinishInferenceRequest).
func FinishInferenceRequest(ctx context.Context, d *sql.DB, id int64, status, response, errText string, durationMS int64) error {
	res, err := d.ExecContext(ctx, `
		UPDATE gb_inference_requests SET status = ?, response = ?, error = ?, duration_ms = ?, completed_at = ? WHERE id = ?`,
		status, response, errText, durationMS, FormatTime(Now()), id)
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
	var created string
	var completed sql.NullString
	if err := s.Scan(&r.ID, &r.TrainedModelID, &r.Prompt, &r.Response, &r.Status, &r.Error, &r.ModelSize, &r.ModelPath,
		&r.MaxTokens, &temp, &r.DurationMS, &created, &completed); err != nil {
		return nil, err
	}
	if temp.Valid {
		r.Temperature = &temp.Float64
	}
	r.CreatedAt = parseStoredTime(created)
	if completed.Valid {
		if t := ParseTime(completed.String); t != nil {
			r.CompletedAt = t
		}
	}
	return &r, nil
}

// GetInferenceRequest returns the request with that ID, or model.ErrNotFound.
func GetInferenceRequest(ctx context.Context, d *sql.DB, id int64) (*model.InferenceRequest, error) {
	r, err := scanInferenceRequest(d.QueryRowContext(ctx, `SELECT `+inferenceRequestColumns+` FROM gb_inference_requests WHERE id = ?`, id))
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
		q += ` WHERE trained_model_id = ?`
		args = append(args, modelID)
	}
	q += ` ORDER BY id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
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
