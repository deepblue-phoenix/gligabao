package model

import (
	"encoding/json"
	"time"
)

// TrainedModel is the result of one completed training job (gb_trained_models): a set of weights (for the fine-tuning the
// sidecar does, a LoRA adapter of a few files) saved on the sidecar's models volume. A job that completes makes a new
// record, so training again, with other data, steps or size, never replaces an earlier model: each can be picked to run
// inference on. The record is made from the "completed" event of the job (see internal/training/monitor.go).
type TrainedModel struct {
	ID int64 `json:"id"`
	// Name is shown in lists; it is made from the training settings when the record is made.
	Name string `json:"name"`
	// RunID is the training job run (gb_training_job_runs) that made the model; nil if the run was deleted since.
	// JobID is the sidecar's id of the job, unique: a job makes one model.
	RunID *int64 `json:"run_id"`
	JobID string `json:"job_id"`
	// BaseModel is the model that was fine-tuned (e.g. "google/gemma-4-E4B-it"), ModelSize its size ("4b", "12b" or "31b"):
	// inference needs the size to pick the hardware.
	BaseModel string `json:"base_model"`
	ModelSize string `json:"model_size"`
	// Dataset is the name of the training dataset file ("" = the sidecar's default one), DatasetSHA256 the digest of the
	// file the job read (the file of that name can be uploaded again with other content) and NumExamples its examples.
	Dataset       string `json:"dataset"`
	DatasetSHA256 string `json:"dataset_sha256"`
	NumExamples   int    `json:"num_examples"`
	// NumTrainSteps is how many steps the job trained, FinalLoss its mean training loss.
	NumTrainSteps int      `json:"num_train_steps"`
	FinalLoss     *float64 `json:"final_loss"`
	// Params are the other training settings, as a JSON object (LoRA rank and alpha, learning rate, batch size, ...).
	Params json.RawMessage `json:"params"`
	// Path is the directory of the weights on the sidecar's models volume, relative to the volume ("trained/<job id>"): it is
	// what inference is asked to load (the sidecar's `model_path`). Files are the files in it, SizeBytes their total size.
	Path      string             `json:"path"`
	Files     []TrainedModelFile `json:"files"`
	SizeBytes int64              `json:"size_bytes"`
	CreatedAt time.Time          `json:"created_at"`
}

// TrainedModelFile is one file of a trained model's weights.
type TrainedModelFile struct {
	Path      string `json:"path"` // relative to the model's directory
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256,omitempty"`
}

// ParamsJSON is Params as stored: a JSON object ("{}" when there are none).
func (m TrainedModel) ParamsJSON() string {
	if len(m.Params) == 0 || !json.Valid(m.Params) {
		return "{}"
	}
	return string(m.Params)
}

// FilesJSON is Files as stored: a JSON array ("[]" when there are none).
func (m TrainedModel) FilesJSON() string {
	b, err := json.Marshal(m.Files)
	if err != nil || m.Files == nil {
		return "[]"
	}
	return string(b)
}

// SetStoredJSON sets Params and Files from the columns they are stored in (an unreadable column leaves the field empty).
func (m *TrainedModel) SetStoredJSON(params, files string) {
	m.Params = json.RawMessage("{}")
	if json.Valid([]byte(params)) {
		m.Params = json.RawMessage(params)
	}
	m.Files = []TrainedModelFile{}
	_ = json.Unmarshal([]byte(files), &m.Files)
	if m.Files == nil {
		m.Files = []TrainedModelFile{}
	}
}
