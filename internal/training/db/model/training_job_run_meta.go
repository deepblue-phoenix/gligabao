package model

import (
	"encoding/json"
	"time"
)

// TrainingJobRunMeta is what a training run was configured with and what it measured (gb_training_job_runs_meta): ONE ROW PER RUN, made
// when the run starts and completed when it ends. gb_training_job_runs is the job (its identity, status, progress and the backend's
// bookkeeping in its Info); this is the training itself, which is meant to grow: the hyperparameters, the split of the data and the
// results that every kind of model has are typed columns, and what only one kind of model has is the JSON Meta, told apart by MetaSchema.
type TrainingJobRunMeta struct {
	// RunID is the run (gb_training_job_runs) the row describes; the row is deleted with it.
	RunID int64 `json:"run_id"`
	// UserID and ModelType are copied from the run, so that the rows can be filtered without a join.
	UserID    string `json:"user_id"`
	ModelType string `json:"model_type"`

	// Hyperparameters that most models have; nil where the model has none or the backend's default was used and is not known.
	// Seed is a 64 bit seed stored as int64 (bit for bit).
	Epochs    *int     `json:"epochs"`
	LR        *float64 `json:"lr"`
	BatchSize *int     `json:"batch_size"`
	Seed      *int64   `json:"seed"`

	// The data: DatasetSHA256 is the digest of the file a job read (LoRA); the examples are how many each set has (graphs for GCNII*),
	// ValidationFraction and SplitSeed how the set was split (nil: not split by the server, or unknown).
	DatasetSHA256         string   `json:"dataset_sha256,omitempty"`
	NumTrainExamples      *int     `json:"num_train_examples"`
	NumValidationExamples *int     `json:"num_validation_examples"`
	ValidationFraction    *float64 `json:"validation_fraction"`
	SplitSeed             *int64   `json:"split_seed"`

	// Results every model can have, set when the run ends (the training loss is the FinalLoss of the run). Nil where none was measured.
	FinalValLoss *float64 `json:"final_val_loss"`

	// MetaSchema names the shape of Meta, "<model type>.v<version>" (e.g. "gcnii.v1"): the code that reads Meta picks the type by it,
	// so a shape can change without a migration. Meta is a JSON object ("{}" when empty), the settings and results of one kind of model.
	MetaSchema string          `json:"meta_schema"`
	Meta       json.RawMessage `json:"meta"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MetaJSON is Meta as stored ("{}" when empty).
func (m TrainingJobRunMeta) MetaJSON() string { return objectOrEmpty(m.Meta) }
