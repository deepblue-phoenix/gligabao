// Package relational opens the database selected in the config and routes every database operation to the backend
// subpackage in use (sqlite or postgres). It covers datasets and the inferences (gb_inferences); graph operations are in internal/db/graph. All SQL lives in the backends (internal/db/relational/sqlite, internal/db/relational/postgres);
// other packages only call the functions here and never write SQL. Each backend owns its schema, its SQL dialect
// and its timestamp storage format.
package relational

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/sqlite"
)

// Types shared with the backends (see package model).
type (
	Dataset               = model.Dataset
	Model                 = model.Model
	Inference             = model.Inference
	InferenceStat         = model.InferenceStat
	InferenceFilter       = model.InferenceFilter
	InferenceGroup        = model.InferenceGroup
	Ontology              = model.Ontology
	Experiment            = model.Experiment
	EvalRef               = model.EvalRef
	ExperimentRun         = model.ExperimentRun
	ExperimentRunEval     = model.ExperimentRunEval
	ExperimentTrajectory  = model.ExperimentTrajectory
	ExperimentEvalPoint   = model.ExperimentEvalPoint
	ExperimentArtifact    = model.ExperimentArtifact
	RememberRun           = model.RememberRun
	RememberStage         = model.RememberStage
	RememberStageDef      = model.RememberStageDef
	RememberRunFilter     = model.RememberRunFilter
	TrainingJobRun        = model.TrainingJobRun
	TrainingJobRunUpdate  = model.TrainingJobRunUpdate
	TrainingJobEvent      = model.TrainingJobEvent
	TrainingDataset       = model.TrainingDataset
	TrainedModel          = model.TrainedModel
	TrainedModelFile      = model.TrainedModelFile
	TrainedModelInference = model.TrainedModelInference
	TrainedModelEval      = model.TrainedModelEval
	TrainedModelEvalItem  = model.TrainedModelEvalItem
)

// Statuses of a TrainedModelEval and of its items.
const (
	TrainedModelEvalRunning      = model.TrainedModelEvalRunning
	TrainedModelEvalCompleted    = model.TrainedModelEvalCompleted
	TrainedModelEvalFailed       = model.TrainedModelEvalFailed
	TrainedModelEvalCancelled    = model.TrainedModelEvalCancelled
	TrainedModelEvalItemJudged   = model.TrainedModelEvalItemJudged
	TrainedModelEvalItemFailed   = model.TrainedModelEvalItemFailed
	TrainedModelEvalItemAnswered = model.TrainedModelEvalItemAnswered
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

// Statuses of a remember run and of its stages, and the stages (see model.RememberStages and gb_remember_runs).
const (
	RememberNotStarted = model.RememberNotStarted
	RememberStarted    = model.RememberStarted
	RememberSucceeded  = model.RememberSucceeded
	RememberFailed     = model.RememberFailed

	RememberStageClassifyComplexity = model.RememberStageClassifyComplexity
	RememberStageChunkText          = model.RememberStageChunkText
	RememberStageExtractGraph       = model.RememberStageExtractGraph
	RememberStageSummarizeChunk     = model.RememberStageSummarizeChunk
	RememberStageLoadCandidates     = model.RememberStageLoadCandidates
	RememberStageResolveEdges       = model.RememberStageResolveEdges
	RememberStageStore              = model.RememberStageStore
	RememberStageStoreSeries        = model.RememberStageStoreSeries
	RememberStageWebSearch          = model.RememberStageWebSearch
	RememberStageFetchSeries        = model.RememberStageFetchSeries
	RememberStageObserveSeries      = model.RememberStageObserveSeries
)

// RememberStages lists the stages of the remember pipeline, in order.
var RememberStages = model.RememberStages

// RememberStageDefOf returns the definition of the stage called name.
var RememberStageDefOf = model.RememberStageDefOf

// NewRememberRun returns a run that has just started, with all its stages not started.
var NewRememberRun = model.NewRememberRun

// Experiment, run and trajectory constants (see package model).
const (
	ExperimentRunning  = model.ExperimentRunning
	ExperimentPassed   = model.ExperimentPassed
	ExperimentFailed   = model.ExperimentFailed
	ExperimentErrored  = model.ExperimentErrored
	TrajectoryRemember = model.TrajectoryRemember
	TrajectoryRecall   = model.TrajectoryRecall
)

// Errors returned by the functions below.
var (
	ErrNotFound = model.ErrNotFound
	ErrExists   = model.ErrExists

	ErrRunNotRunning = model.ErrRunNotRunning
)

//---------------------------------------------------------------------------------------------

// active is the backend selected by the last successful Open.
var active atomic.Value // string

func backend() string {
	if b, _ := active.Load().(string); b != "" {
		return b
	}
	return config.DBBackendSQLite
}

func unsupported() error { return fmt.Errorf("unknown db backend %q", backend()) }

// Open opens the database of the given backend (config.DBBackendSQLite or config.DBBackendPostgres; empty means
// sqlite), applies its schema, and makes it the backend all other functions of this package route to.
// For sqlite, dsn is the database file path; for postgres it is a connection URL.
func Open(backendName, dsn string) (*sql.DB, error) {
	if backendName == "" {
		backendName = config.DBBackendSQLite
	}
	var (
		d   *sql.DB
		err error
	)
	switch backendName {
	case config.DBBackendSQLite:
		d, err = sqlite.Open(dsn)
	case config.DBBackendPostgres:
		d, err = postgres.Open(dsn)
	default:
		return nil, fmt.Errorf("unknown db backend %q", backendName)
	}
	if err != nil {
		return nil, err
	}
	active.Store(backendName)
	return d, nil
}

//---------------------------------------------------------------------------------------------
// Datasets

// CreateDataset creates a dataset owned by userID (inputs already validated). It returns ErrExists if the user
// already has a live dataset with that name.
func CreateDataset(ctx context.Context, d *sql.DB, userID, name, description string, tags []string) (*Dataset, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.CreateDataset(ctx, d, userID, name, description, tags)
	case config.DBBackendPostgres:
		return postgres.CreateDataset(ctx, d, userID, name, description, tags)
	}
	return nil, unsupported()
}

// ListDatasets returns userID's datasets oldest first; deleted ones only if includeDeleted.
func ListDatasets(ctx context.Context, d *sql.DB, userID string, includeDeleted bool) ([]Dataset, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListDatasets(ctx, d, userID, includeDeleted)
	case config.DBBackendPostgres:
		return postgres.ListDatasets(ctx, d, userID, includeDeleted)
	}
	return nil, unsupported()
}

// ListAllDatasets returns the datasets of all users, oldest first; deleted ones only if includeDeleted.
// It is meant for admin tooling; the user-facing API only ever lists a single user's datasets.
func ListAllDatasets(ctx context.Context, d *sql.DB, includeDeleted bool) ([]Dataset, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListAllDatasets(ctx, d, includeDeleted)
	case config.DBBackendPostgres:
		return postgres.ListAllDatasets(ctx, d, includeDeleted)
	}
	return nil, unsupported()
}

// DeleteDataset soft-deletes userID's live dataset called name. It returns ErrNotFound if there is none.
func DeleteDataset(ctx context.Context, d *sql.DB, userID, name string) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.DeleteDataset(ctx, d, userID, name)
	case config.DBBackendPostgres:
		return postgres.DeleteDataset(ctx, d, userID, name)
	}
	return unsupported()
}

// DatasetID returns the ID of userID's live dataset called name, or ErrNotFound.
func DatasetID(ctx context.Context, d *sql.DB, userID, name string) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.DatasetID(ctx, d, userID, name)
	case config.DBBackendPostgres:
		return postgres.DatasetID(ctx, d, userID, name)
	}
	return 0, unsupported()
}

// EnsureDataset returns the ID of userID's live dataset called name, creating it first if needed.
func EnsureDataset(ctx context.Context, d *sql.DB, userID, name string) (int64, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.EnsureDataset(ctx, d, userID, name)
	case config.DBBackendPostgres:
		return postgres.EnsureDataset(ctx, d, userID, name)
	}
	return 0, unsupported()
}

//---------------------------------------------------------------------------------------------
// Ontologies (metadata only; the classes and relationships are stored by package graph)

// CreateOntology stores the metadata of an ontology (o.ID and o.CreatedAt are ignored and filled in the result).
// It returns ErrExists if the user already has an ontology with that name.
func CreateOntology(ctx context.Context, d *sql.DB, o Ontology) (*Ontology, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.CreateOntology(ctx, d, o)
	case config.DBBackendPostgres:
		return postgres.CreateOntology(ctx, d, o)
	}
	return nil, unsupported()
}

// GetOntology returns the metadata of userID's ontology called name, or ErrNotFound.
func GetOntology(ctx context.Context, d *sql.DB, userID, name string) (*Ontology, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.GetOntology(ctx, d, userID, name)
	case config.DBBackendPostgres:
		return postgres.GetOntology(ctx, d, userID, name)
	}
	return nil, unsupported()
}

// UpdateOntology overwrites the metadata of the ontology o.ID with the other fields of o (description, IRI, title,
// format, size, hash, counts and UpdatedAt); the owner, name and creation time never change. It returns the stored
// row, or ErrNotFound.
func UpdateOntology(ctx context.Context, d *sql.DB, o Ontology) (*Ontology, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.UpdateOntology(ctx, d, o)
	case config.DBBackendPostgres:
		return postgres.UpdateOntology(ctx, d, o)
	}
	return nil, unsupported()
}

// ListOntologies returns userID's ontologies, oldest first.
func ListOntologies(ctx context.Context, d *sql.DB, userID string) ([]Ontology, error) {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.ListOntologies(ctx, d, userID)
	case config.DBBackendPostgres:
		return postgres.ListOntologies(ctx, d, userID)
	}
	return nil, unsupported()
}

// DeleteOntology removes the metadata row of an ontology (and, through the foreign keys, whatever graph rows
// still reference it). It is used to undo a failed load.
func DeleteOntology(ctx context.Context, d *sql.DB, id int64) error {
	switch backend() {
	case config.DBBackendSQLite:
		return sqlite.DeleteOntology(ctx, d, id)
	case config.DBBackendPostgres:
		return postgres.DeleteOntology(ctx, d, id)
	}
	return unsupported()
}
