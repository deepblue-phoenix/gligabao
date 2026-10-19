package model

import "time"

// TrainingDataset is the record (gb_training_datasets) of a training dataset: a JSONL file in a subdirectory of the
// training datasets directory, which a dataset.toml manifest there describes (name, where the file is). The record is made
// when the Go server first sees the file, or sees it changed; it holds what was seen (size, records, digest). The sidecar
// is not involved: the file is sent to it when a training job that uses the dataset is started, and SyncedAt records that.
type TrainingDataset struct {
	ID int64 `json:"id"`
	// Filename is the dataset's name plus ".jsonl": the file name the dataset has on the sidecar. It is unique (dataset
	// names are), so a dataset's record is replaced when its file changes.
	Filename   string `json:"filename"`
	SizeBytes  int64  `json:"size_bytes"`
	NumRecords int    `json:"num_records"` // non-empty lines (each a JSON object)
	SHA256     string `json:"sha256"`      // hex digest of the file
	// Status is TrainingDatasetStored (only on the Go server), or TrainingDatasetSynced once the sidecar has this version
	// of the file.
	Status string `json:"status"`
	// CreatedAt is when the record was first made; UpdatedAt when the file was last uploaded; SyncedAt when the sidecar got
	// the current file (nil while it has not).
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	SyncedAt  *time.Time `json:"synced_at"`
}

// Statuses of a TrainingDataset.
const (
	TrainingDatasetStored = "stored"
	TrainingDatasetSynced = "synced"
)
