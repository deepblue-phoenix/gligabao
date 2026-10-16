package sidecar

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TrainingDataset is a training dataset on the sidecar (side_car/ai_train/api/models.py: TrainingDataset).
type TrainingDataset struct {
	Filename   string     `json:"filename"`
	SizeBytes  *int64     `json:"size_bytes,omitempty"`
	UploadedAt *time.Time `json:"uploaded_at,omitempty"`
	ModifiedAt *time.Time `json:"modified_at,omitempty"`
}

type uploadTrainingDatasetRequest struct {
	Filename string `json:"filename"`
	Content  string `json:"content"` // base64
}

// UploadTrainingDatasetResponse is the body of POST /datasets/upload.
type UploadTrainingDatasetResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	SizeBytes int64  `json:"size_bytes"`
}

// UploadTrainingDataset sends a training dataset (jsonl bytes) to the sidecar via POST /datasets/upload.
func (c *Client) UploadTrainingDataset(ctx context.Context, filename string, content []byte) (UploadTrainingDatasetResponse, error) {
	var out UploadTrainingDatasetResponse
	if strings.TrimSpace(filename) == "" || len(content) == 0 {
		return out, errors.New("sidecar: filename and content are required")
	}
	err := c.post(ctx, "/datasets/upload", uploadTrainingDatasetRequest{
		Filename: filename,
		Content:  base64.StdEncoding.EncodeToString(content),
	}, &out)
	return out, err
}

// ListTrainingDatasets returns the training datasets on the sidecar via GET /datasets/list.
func (c *Client) ListTrainingDatasets(ctx context.Context) ([]TrainingDataset, error) {
	var out struct {
		Datasets []TrainingDataset `json:"datasets"`
	}
	if err := c.get(ctx, "/datasets/list", &out); err != nil {
		return nil, err
	}
	return out.Datasets, nil
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sidecar POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Method: http.MethodPost, Path: path, Code: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("sidecar POST %s: decode: %w", path, err)
	}
	return nil
}
