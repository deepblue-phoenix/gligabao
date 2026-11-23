package model

import (
	"encoding/json"
	"time"
)

// The types of trained model (TrainedModel.ModelType), which are also the "<model type>" directory of the weights in the store of
// the weights ("model_weights/<model type>/...").
const (
	// ModelTypeLoRA is a LoRA adapter the sidecar fine-tuned (the default, and the type of every row made before types existed).
	ModelTypeLoRA = "lora"
	// ModelTypeGCNII is a GCNII* graph classifier that sargo trained for one user.
	ModelTypeGCNII = "gcnii"
)

// Where the weights of a trained model are kept (TrainedModel.Storage).
const (
	// StorageModalVolume is the sidecar's models volume (Modal); Path is relative to it.
	StorageModalVolume = "modal_volume"
	// StorageS3 is an S3 bucket ("<prefix>/<model type>/<user>/<job id>/"); Path is that directory, without the bucket.
	StorageS3 = "s3"
	// StorageLocal is a local directory of the server (the store of the weights when there is no bucket); Path is relative to it.
	StorageLocal = "local"
)

// TrainedModel is the result of one completed training job (gb_trained_models): a set of weights (for the fine-tuning the
// sidecar does, a LoRA adapter of a few files) saved on the sidecar's models volume, or (the GCNII* models of sargo) in S3. A job that completes makes a new
// record, so training again, with other data, steps or size, never replaces an earlier model: each can be picked to run
// inference on. The record is made from the "completed" event of the job (see internal/training/monitor.go).
type TrainedModel struct {
	ID int64 `json:"id"`
	// ModelType is the kind of model (ModelTypeLoRA, ModelTypeGCNII) and decides what the other fields mean: a LoRA model has a
	// BaseModel and ModelSize and runs on the sidecar; a GCNII* model belongs to UserID, runs in sargo and has no base model.
	ModelType string `json:"model_type"`
	// UserID is the user the model belongs to ("" for a model that belongs to nobody: the LoRA models of the training API).
	UserID string `json:"user_id"`
	// Storage is where the weights are (StorageModalVolume, StorageS3, StorageLocal).
	Storage string `json:"storage"`
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
	// Path is the directory of the weights in Storage: for the sidecar's models volume it is relative to the volume ("trained/<job id>")
	// and is what inference is asked to load (the sidecar's `model_path`); for S3 it is the key of the directory
	// ("model_weights/gcnii/<user>/<job id>"). Files are the files in it, SizeBytes their total size.
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

// WithDefaults fills the fields a caller may leave empty: the type is LoRA and the weights are on the sidecar's volume.
func (m TrainedModel) WithDefaults() TrainedModel {
	if m.ModelType == "" {
		m.ModelType = ModelTypeLoRA
	}
	if m.Storage == "" {
		m.Storage = StorageModalVolume
	}
	return m
}

// RunsOnSidecar says whether the model is run (inference, evals, audits, deployment) through the sidecar: only the LoRA models are.
func (m TrainedModel) RunsOnSidecar() bool { return m.ModelType == "" || m.ModelType == ModelTypeLoRA }

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
func (m *TrainedModel) SetStoredJSON(params,
	files string) {
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
