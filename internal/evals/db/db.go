// Package db stores and queries the evals: the experiments, runs and case results of the knowledge evals, and the eval and
// audit runs of trained models (see internal/evals/README.md). Every operation goes through the functions here, which
// route to the backend in use (db/sqlite or db/postgres). The backends keep the eval tables in the relational database
// opened by package relational (they reference its datasets and trained models), so Open must be called with the *sql.DB
// returned by relational.Open. All SQL lives in the backends; other packages only call the functions here and never
// write SQL.
package db

import (
	"database/sql"
	"fmt"
	"sync/atomic"

	"github.com/deepblue-phoenix/gligabao/internal/config"
	relmodel "github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db/model"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db/sqlite"
)

// Types shared with the backends (see package model).
type (
	Experiment           = model.Experiment
	CaseRef              = model.CaseRef
	ExperimentRun        = model.ExperimentRun
	ExperimentCaseResult = model.ExperimentCaseResult
	ExperimentTrajectory = model.ExperimentTrajectory
	ExperimentArtifact   = model.ExperimentArtifact

	TrainedModelRun  = model.TrainedModelRun
	TrainedModelCase = model.TrainedModelCase

	TrainingExperiment      = model.TrainingExperiment
	TrainingExperimentRun   = model.TrainingExperimentRun
	TrainingExperimentModel = model.TrainingExperimentModel

	// Dataset is the dataset a knowledge case ran in (see GetExperimentCaseResultDataset).
	Dataset = relmodel.Dataset
)

// Statuses of the runs and cases of a trained model, shared by evals and audits (see model/trained_model_status.go).
const (
	TrainedModelRunRunning   = model.TrainedModelRunRunning
	TrainedModelRunCompleted = model.TrainedModelRunCompleted
	TrainedModelRunErrored   = model.TrainedModelRunErrored
	TrainedModelRunCancelled = model.TrainedModelRunCancelled
	TrainedModelCaseJudged   = model.TrainedModelCaseJudged
	TrainedModelCaseErrored  = model.TrainedModelCaseErrored
	TrainedModelCaseAnswered = model.TrainedModelCaseAnswered
)

// The kinds of TrainedModelRun.
const (
	TrainedModelKindEval  = model.TrainedModelKindEval
	TrainedModelKindAudit = model.TrainedModelKindAudit
)

// Experiment, run and trajectory constants (see package model).
const (
	EvalTypeKnowledge  = model.EvalTypeKnowledge
	EvalTypeTraining   = model.EvalTypeTraining
	ExperimentRunning  = model.ExperimentRunning
	ExperimentPassed   = model.ExperimentPassed
	ExperimentFailed   = model.ExperimentFailed
	ExperimentErrored  = model.ExperimentErrored
	TrajectoryRemember = model.TrajectoryRemember
	TrajectoryRecall   = model.TrajectoryRecall
)

// Errors returned by the functions of this package. ErrNotFound is the same error as relational.ErrNotFound.
var (
	ErrNotFound      = model.ErrNotFound
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

func unsupported() error { return fmt.Errorf("unknown evals db backend %q", backend()) }

// Open prepares the eval store of the given backend (config.DBBackendSQLite or config.DBBackendPostgres; empty means
// sqlite) and makes it the backend all other functions of this package route to. The backends keep the evals in the
// database d they were opened with, so call it with the *sql.DB returned by relational.Open (whose schema the eval tables
// reference), using the same backend name.
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
		return fmt.Errorf("unknown evals db backend %q", backendName)
	}
	if err != nil {
		return err
	}
	active.Store(backendName)
	return nil
}
