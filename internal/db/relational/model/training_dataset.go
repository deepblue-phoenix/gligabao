package model

import "time"

// TrainingDataset is a JSONL training dataset uploaded through the Go server (gb_training_datasets). The file itself is kept
// by Go in the training dataset directory (under Filename); this is its record. The sidecar is not involved in the upload:
// the file is sent to it later, when a training job that uses the dataset is started, and SyncedAt records that.
type TrainingDataset struct {
	ID int64 `json:"id"`
	// Filename is a plain *.jsonl file name; it names the file in the training dataset directory and the dataset on the
	// sidecar. It is unique: uploading the same name again replaces the dataset.
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
