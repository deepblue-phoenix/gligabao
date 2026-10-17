package model

import "time"

// Statuses of an InferenceRequest.
const (
	InferenceRunning   = "running"   // the request is with the sidecar
	InferenceCompleted = "completed" // the model answered: Response
	InferenceFailed    = "failed"    // it did not: Error says why
)

// InferenceRequest is one request to run a prompt through a trained model (gb_inference_requests). It is stored when the
// request starts (status "running") and updated when the sidecar answers or fails, so every request can be reviewed later,
// including the ones that failed or are still running (or were interrupted by a restart of the server).
type InferenceRequest struct {
	ID int64 `json:"id"`
	// TrainedModelID is the trained model (gb_trained_models) the prompt was run through.
	TrainedModelID int64  `json:"trained_model_id"`
	Prompt         string `json:"prompt"`
	// Response is the model's reply ("" until the request completes); Error the reason when Status is "failed".
	Response string `json:"response"`
	Status   string `json:"status"`
	Error    string `json:"error"`
	// ModelSize and ModelPath are what the sidecar was asked to load (copied from the trained model when the request is made).
	ModelSize string `json:"model_size"`
	ModelPath string `json:"model_path"`
	// MaxTokens is the limit asked for (0 = the sidecar's default), Temperature the sampling temperature (nil = its default).
	MaxTokens   int      `json:"max_tokens"`
	Temperature *float64 `json:"temperature"`
	// DurationMS is how long the request took (0 while it runs); CompletedAt when it ended (nil while it runs).
	DurationMS  int64      `json:"duration_ms"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at"`
}
