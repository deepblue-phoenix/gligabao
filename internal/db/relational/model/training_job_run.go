package model

import "time"

// TrainingJobRun is one training job started on the sidecar (gb_training_job_runs): a record is made for each job the
// sidecar reports as started (see internal/sidecar: TrainingJob), and updated as the job's progress events arrive.
type TrainingJobRun struct {
	ID int64 `json:"id"`
	// JobID is the sidecar's ID of the job (it names the job's progress file there); ProviderJobID is the compute
	// provider's ID of the spawned call.
	JobID         string `json:"job_id"`
	ProviderJobID string `json:"provider_job_id,omitempty"`
	// Status is "started" when the sidecar reports the job started, then follows the job's status events
	// ("running", "training") until it is "completed" or "failed" (see TrainingJobFinished).
	Status    string `json:"status"`
	ModelSize string `json:"model_size"` // "4b" or "12b"
	// Dataset is the file the job trains on, on the sidecar's data volume ("" = the sidecar's default dataset).
	Dataset string `json:"dataset"`
	// NumTrainSteps is how many steps the job was asked to train.
	NumTrainSteps int `json:"num_train_steps"`
	// LastStep and LastLoss are from the newest progress event that has a loss (LastLoss is nil before that).
	LastStep int      `json:"last_step"`
	LastLoss *float64 `json:"last_loss"`
	// FinalLoss is the mean training loss, reported when the job completes. Error is why it failed.
	FinalLoss *float64 `json:"final_loss"`
	Error     string   `json:"error,omitempty"`
	// StartedAt is when the sidecar says the job started; CreatedAt is when this record was made; FinishedAt is when the
	// job completed or failed (nil while it runs).
	StartedAt  time.Time  `json:"started_at"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// Statuses of a training job that end it.
const (
	TrainingJobCompleted = "completed"
	TrainingJobFailed    = "failed"
)

// TrainingJobFinished reports whether status ends a job.
func TrainingJobFinished(status string) bool {
	return status == TrainingJobCompleted || status == TrainingJobFailed
}

// TrainingJobRunUpdate is a change to a TrainingJobRun: only the fields that are set (non-zero) are written.
type TrainingJobRunUpdate struct {
	Status    string
	Step      *int     // with Loss: the newest progress
	Loss      *float64 // ignored without Step
	FinalLoss *float64
	Error     string
	// FinishedAt marks the job finished at that time.
	FinishedAt *time.Time
}

// TrainingJobEvent is one event of a training job as the sidecar reported it (gb_training_job_events). Seq is its index
// in the sidecar's progress file for the job; Data is the event as JSON (see internal/sidecar: TrainingEvent).
type TrainingJobEvent struct {
	RunID     int64     `json:"run_id"`
	Seq       int       `json:"seq"`
	Type      string    `json:"type"` // "status" or "progress"
	Data      string    `json:"data"`
	CreatedAt time.Time `json:"created_at"`
}
