package sidecar

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Model sizes the sidecar can train (side_car/ai_train/ai_train_core/model_config.py: MODEL_SIZES).
const (
	ModelSize4B  = "4b"
	ModelSize12B = "12b"
)

// DefaultNumTrainSteps and DefaultModelSize are what the sidecar uses for the fields StartTrainingJobRequest leaves unset.
const (
	DefaultNumTrainSteps = 100
	DefaultModelSize     = ModelSize4B
)

// TrainingJob is a training job started on the sidecar (side_car/ai_train/api/models.py: TrainingJob). The sidecar
// reports it back when a job is started.
type TrainingJob struct {
	// JobID is the compute provider's ID of the spawned job.
	JobID  string `json:"job_id"`
	Status string `json:"status"` // "started"
	// ModelSize is "4b" or "12b".
	ModelSize     string    `json:"model_size"`
	NumTrainSteps int       `json:"num_train_steps"`
	StartedAt     time.Time `json:"started_at"`
}

// StartTrainingJobRequest is the body of POST /training/job_start. Zero fields take the sidecar's defaults
// (DefaultNumTrainSteps, DefaultModelSize).
type StartTrainingJobRequest struct {
	NumTrainSteps int    `json:"num_train_steps,omitempty"`
	ModelSize     string `json:"model_size,omitempty"`
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
