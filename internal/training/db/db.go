// Package db stores and queries the training data: the training jobs the sidecar runs and their events, the models they
// trained, the inferences run on those, and the training datasets. Every operation goes through the functions here, which
// route to the backend in use (db/sqlite or db/postgres). The backends keep the training tables in the relational database
// opened by package relational (the tables of the evals, internal/evals/db, reference the trained models), so Open must be
// called with the *sql.DB returned by relational.Open, before evalsdb.Open. All SQL lives in the backends; other packages
// only call the functions here and never write SQL.
package db

import (
	"database/sql"
	"fmt"
	"sync/atomic"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/model"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/sqlite"
)

// Types shared with the backends (see package model).
type (
	TrainingJobRun        = model.TrainingJobRun
	TrainingJobRunUpdate  = model.TrainingJobRunUpdate
	TrainingJobEvent      = model.TrainingJobEvent
	TrainingDataset       = model.TrainingDataset
	TrainedModel          = model.TrainedModel
	TrainedModelFile      = model.TrainedModelFile
	TrainedModelInference = model.TrainedModelInference
)

// Statuses of a TrainedModelInference.
const (
	TrainedModelInferenceRunning   = model.TrainedModelInferenceRunning
	TrainedModelInferenceCompleted = model.TrainedModelInferenceCompleted
	TrainedModelInferenceFailed    = model.TrainedModelInferenceFailed
)

// Statuses of a TrainingDataset.
const (
	TrainingDatasetStored = model.TrainingDatasetStored
	TrainingDatasetSynced = model.TrainingDatasetSynced
)

// Statuses that end a training job (see model.TrainingJobRun).
const (
	TrainingJobCompleted = model.TrainingJobCompleted
	TrainingJobFailed    = model.TrainingJobFailed
	TrainingJobCancelled = model.TrainingJobCancelled
)

// TrainingJobFinished reports whether a training job status ends the job.
func TrainingJobFinished(status string) bool { return model.TrainingJobFinished(status) }

// ErrNotFound is returned by the functions of this package when a row does not exist. It is the same error as
// relational.ErrNotFound.
var ErrNotFound = model.ErrNotFound

//---------------------------------------------------------------------------------------------

// active is the backend selected by the last successful Open.
var active atomic.Value // string

func backend() string {
	if b, _ := active.Load().(string); b != "" {
		return b
	}
	return config.DBBackendSQLite
}

func unsupported() error { return fmt.Errorf("unknown training db backend %q", backend()) }

// Open prepares the training store of the given backend (config.DBBackendSQLite or config.DBBackendPostgres; empty means
// sqlite) and makes it the backend all other functions of this package route to. The backends keep the training tables in
// the database d they were opened with, so call it with the *sql.DB returned by relational.Open, using the same backend name.
func Open(backendName string, d *sql.DB) error {
	if backendName == "" {
		backendName = config.DBBackendSQLite
	}
	var err error
	switch backendName {
	case config.DBBackendSQLite:
		err = sqlite.Open(d)
	case config.DBBackendPostgres:
		err = postgres.Open(d)
	default:
		return fmt.Errorf("unknown training db backend %q", backendName)
	}
	if err != nil {
		return err
	}
	active.Store(backendName)
	return nil
}
