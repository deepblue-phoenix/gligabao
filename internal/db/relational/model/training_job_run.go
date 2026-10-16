package model

import "time"

// TrainingJobRun is one training job started on the sidecar (gb_training_job_runs): a record is made for each job the
// sidecar reports as started (see internal/sidecar: TrainingJob).
type TrainingJobRun struct {
	ID int64 `json:"id"`
	// JobID is the compute provider's ID of the job, as the sidecar reported it.
	JobID string `json:"job_id"`
	// Status is the status the sidecar reported when the job started ("started").
	Status    string `json:"status"`
	ModelSize string `json:"model_size"` // "4b" or "12b"
	// NumTrainSteps is how many steps the job was asked to train.
	NumTrainSteps int `json:"num_train_steps"`
	// StartedAt is when the sidecar says the job started; CreatedAt is when this record was made.
	StartedAt time.Time `json:"started_at"`
	CreatedAt time.Time `json:"created_at"`
}
