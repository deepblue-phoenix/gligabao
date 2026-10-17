package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Model sizes the sidecar can train (sidecar/ai_train/ai_train_core/model_config.py: MODEL_SIZES).
const (
	ModelSize4B  = "4b"
	ModelSize12B = "12b"
)

// DefaultNumTrainSteps and DefaultModelSize are what the sidecar uses for the fields StartTrainingJobRequest leaves unset.
const (
	DefaultNumTrainSteps = 100
	DefaultModelSize     = ModelSize4B
)

// TrainingJob is a training job started on the sidecar (sidecar/ai_train/api/models.py: TrainingJob). The sidecar
// reports it back when a job is started.
type TrainingJob struct {
	// JobID is the sidecar's ID of the job: it names the job's event log there (see StreamTrainingJobEvents).
	// ProviderJobID is the compute provider's ID of the spawned call.
	JobID         string `json:"job_id"`
	ProviderJobID string `json:"provider_job_id,omitempty"`
	Status        string `json:"status"` // "started"
	// ModelSize is "4b" or "12b".
	ModelSize     string `json:"model_size"`
	NumTrainSteps int    `json:"num_train_steps"`
	// Dataset is the file on the sidecar's data volume the job trains on ("" = the sidecar's default dataset).
	Dataset   string    `json:"dataset,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// StartTrainingJobRequest is the body of POST /training/job_start. Zero fields take the sidecar's defaults
// (DefaultNumTrainSteps, DefaultModelSize, its default dataset).
type StartTrainingJobRequest struct {
	NumTrainSteps int    `json:"num_train_steps,omitempty"`
	ModelSize     string `json:"model_size,omitempty"`
	// Dataset is the file name of a training dataset ("" = the sidecar's default dataset).
	Dataset string `json:"dataset,omitempty"`
	// DatasetContent is the dataset's jsonl file, base64 encoded. The sidecar has no dataset endpoints (the Go server owns
	// the datasets); it stores the file internally under the name Dataset before it starts the job. Without it Dataset
	// must be on the sidecar's data volume already.
	DatasetContent string `json:"dataset_content,omitempty"`
}

// StartTrainingJob starts a training job on the sidecar via POST /training/job_start and returns the job it reports.
func (c *Client) StartTrainingJob(ctx context.Context, req StartTrainingJobRequest) (TrainingJob, error) {
	var out TrainingJob
	if req.NumTrainSteps < 0 {
		return out, errors.New("sidecar: num_train_steps must be positive")
	}
	if err := c.post(ctx, "/training/job_start", req, &out); err != nil {
		return out, err
	}
	if strings.TrimSpace(out.JobID) == "" {
		return out, errors.New("sidecar: job_start returned no job_id")
	}
	return out, nil
}

// CancelTrainingJob asks the sidecar to stop a training job (POST /training/job_cancel). The job ends with a "cancelled"
// event at its next step, not at once. The sidecar answers 404 for a job it does not know and 409 for one that is over
// (a *StatusError).
func (c *Client) CancelTrainingJob(ctx context.Context, jobID string) error {
	if strings.TrimSpace(jobID) == "" {
		return errors.New("sidecar: job_id is required")
	}
	var out struct {
		Status string `json:"status"`
	}
	return c.post(ctx, "/training/job_cancel", map[string]string{"job_id": jobID}, &out)
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	return c.postVia(ctx, c.http, path, in, out)
}

// postVia is post with another HTTP client: c.stream for a call that takes long (it has no timeout of its own).
func (c *Client) postVia(ctx context.Context, hc *http.Client, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
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
