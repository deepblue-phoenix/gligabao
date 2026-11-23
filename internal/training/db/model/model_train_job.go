package model

import (
	"encoding/json"
	"errors"
	"time"
)

// The states of a ModelTrainJob.
const (
	ModelTrainJobRunning = "running"
	ModelTrainJobDone    = "done"
	ModelTrainJobFailed  = "failed"
	// ModelTrainJobLost is a job whose instance stopped beating while it ran (it crashed, or was replaced): nobody is training it.
	ModelTrainJobLost = "lost"
)

// ErrModelTrainJobRunning is returned when a user already has a running training of that model type.
var ErrModelTrainJobRunning = errors.New("a training of this user is already running")

// ModelTrainJob is the training of a model that runs outside the sidecar, in the local sargo of ONE gligabao instance (gb_model_train_jobs;
// today the GCNII* models). The instances of a cluster share the database, not sargo, so this row is what the others see: that a
// training is running and on which instance, how it ended, and the model it made. The instance that runs it beats (HeartbeatAt) while it
// follows the job; a running job without a recent beat is lost.
type ModelTrainJob struct {
	ID int64 `json:"id"`
	// JobID is the ID of the model the job makes (TrainedModel.JobID), chosen when the job starts.
	JobID     string `json:"job_id"`
	ModelType string `json:"model_type"`
	UserID    string `json:"user_id"`
	// InstanceID is the gligabao process that runs the job (it is the only one whose sargo has the weights).
	InstanceID string `json:"instance_id"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	// Job is the last state of the job as sargo reported it (sargo.GCNIIJob), Info what the request knew (dataset, history run, settings).
	Job  json.RawMessage `json:"job"`
	Info json.RawMessage `json:"info"`
	// ModelID is the trained model (gb_trained_models) of a done job.
	ModelID     *int64     `json:"model_id"`
	StartedAt   time.Time  `json:"started_at"`
	HeartbeatAt time.Time  `json:"heartbeat_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

// JobJSON and InfoJSON are Job and Info as stored ("{}" when empty).
func (j ModelTrainJob) JobJSON() string  { return objectOrEmpty(j.Job) }
func (j ModelTrainJob) InfoJSON() string { return objectOrEmpty(j.Info) }

func objectOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 || !json.Valid(raw) {
		return "{}"
	}
	return string(raw)
}

// SetStoredJSON sets Job and Info from the columns they are stored in.
func (j *ModelTrainJob) SetStoredJSON(job,
	info string) {
	j.Job = json.RawMessage(objectOrEmpty(json.RawMessage(job)))
	j.Info = json.RawMessage(objectOrEmpty(json.RawMessage(info)))
}
