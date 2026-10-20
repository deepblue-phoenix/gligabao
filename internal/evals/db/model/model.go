// Package model holds the types shared by package evals/db and its backends (sqlite, postgres): the records they read and
// write. The records of the evals of trained models (trained_model_run.go, trained_model_status.go, training_experiment.go)
// and of the knowledge evals (this file). Every backend can import it. ErrNotFound is the one of
// internal/db/relational/model, so one error means "not found" in both.
package model

import (
	"errors"
	"time"

	relmodel "github.com/deepblue-phoenix/gligabao/internal/db/relational/model"
)

// ErrNotFound means the requested row does not exist. It is relational's model.ErrNotFound.
var ErrNotFound = relmodel.ErrNotFound

// ErrRunNotRunning means results were recorded for an experiment run that is already finished.
var ErrRunNotRunning = errors.New("experiment run is not running")

//---------------------------------------------------------------------------------------------
// Eval experiments

// Eval types: the type column of the universal eval tables (gb_experiments_defs, gb_experiment_cases_defs, gb_experiment_runs and
// gb_experiment_case_results, see sqlite/db_schema.go). What is only one kind's is in its metadata table
// (gb_knowledge_case_meta, gb_training_case_meta, gb_training_eval_run_meta).
const (
	// EvalTypeKnowledge is the type of the knowledge-extraction evals: remember, then recall, checked programmatically
	// (internal/evals/knowledge, package experiments).
	EvalTypeKnowledge = "knowledge"
	// EvalTypeTraining is the type of the evals of trained models (internal/evals/training, internal/api/training): an eval run of a
	// trained model, whose run has one case result per example of the eval (case_name = the example's index in the dataset).
	EvalTypeTraining = "training"
)

// Run and case statuses of the KNOWLEDGE evals (the Experiment* constants). The statuses of the trained-model evals and audits
// are their own (TrainedModelRun* and TrainedModelCase*, see trained_model_status.go), because they mean other things (a training
// score is not a verdict). The CHECKs of the schema enforce the set of each type:
//
//	knowledge run:  running, then passed (every case passed) or failed (at least one did not)
//	knowledge case: passed, failed (a check did not hold) or error (could not be run to the end)
//	training run:   running, then completed (it ran to the end), error (the run itself errored) or cancelled
//	training case:  judged (the model answered and the judge scored it; a score of 0 too) or error (the model or the judge errored)
//
// "failed" is a verdict about checks, so only knowledge has it; "error" means "could not be run to the end" wherever it is used.
const (
	// ExperimentRunning is the status of a run that has not been finished yet.
	ExperimentRunning = "running"
	// ExperimentPassed is the status of a finished run whose cases all passed, and of a passed case.
	ExperimentPassed = "passed"
	// ExperimentFailed is the status of a finished run with at least one case that did not pass, and of a case whose checks
	// (temporal facts, expected answer) did not hold.
	ExperimentFailed = "failed"
	// ExperimentErrored is the status of a case that could not be run to the end (server, LLM or API error).
	ExperimentErrored = "error"
)

// Trajectory kinds: the operation whose ATIF trajectory it is.
const (
	TrajectoryRemember = "remember"
	TrajectoryRecall   = "recall"
)

// Experiment is a named series of eval runs. Its runs (ExperimentRun) are what was actually executed; Cases lists
// every case (eval + case name) any of its runs executed. Names are unique.
type Experiment struct {
	ID          int64          `json:"id"`
	Type        string         `json:"type"` // EvalTypeKnowledge
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Config      map[string]any `json:"config"` // free-form settings under test (models, prompts, ...)
	CreatedAt   time.Time      `json:"created_at"`
	// Cases are the cases the experiment's runs executed, ordered by eval and name. Filled by GetExperiment only.
	Cases []CaseRef `json:"cases,omitempty"`
}

// CaseRef identifies a case: its case name and the eval (the eval file name without ".json") that holds it.
type CaseRef struct {
	Eval string `json:"eval"`
	Name string `json:"name"`
}

// ExperimentRun is one execution of an experiment: a pass over some of the cases in the evals. Each of the four counts is of
// the cases with the status of its name and nothing else; TotalCases minus the four are the cases not reached (a cancelled run).
type ExperimentRun struct {
	ID           int64                  `json:"id"`
	Type         string                 `json:"type"` // EvalTypeKnowledge
	ExperimentID int64                  `json:"experiment_id"`
	Status       string                 `json:"status"`       // ExperimentRunning, ExperimentPassed or ExperimentFailed
	Params       map[string]any         `json:"params"`       // how the run was invoked (evals dir, selected cases, ...)
	TotalCases   int                    `json:"total_cases"`  // set when the run is finished
	PassedCases  int                    `json:"passed_cases"` // the cases with status passed (knowledge); set when the run is finished
	FailedCases  int                    `json:"failed_cases"` // the cases with status failed: a check did not hold (knowledge)
	ErrorCases   int                    `json:"error_cases"`  // the cases with status error: could not be run to the end
	JudgedCases  int                    `json:"judged_cases"` // the cases with status judged (training: scored, whatever the score)
	CostUSD      float64                `json:"cost_usd"`     // LLM cost of all cases; set when the run is finished
	StartedAt    time.Time              `json:"started_at"`   //
	FinishedAt   *time.Time             `json:"finished_at,omitempty"`
	CaseResults  []ExperimentCaseResult `json:"case_results,omitempty"` // filled by GetExperimentRun only
}

// ExperimentCaseResult is the result of one case within a run: the outcome of that case of an eval in that run (a case is one
// input plus what is checked, with its metadata).
type ExperimentCaseResult struct {
	ID       int64    `json:"id"`
	Type     string   `json:"type"` // EvalTypeKnowledge
	RunID    int64    `json:"run_id"`
	Eval     string   `json:"eval"`
	Name     string   `json:"name"`
	Status   string   `json:"status"` // ExperimentPassed, ExperimentFailed (a check did not hold) or ExperimentErrored
	Answer   string   `json:"answer,omitempty"`
	Problems []string `json:"problems"` // failed checks
	// PassedChecks are the checks that held (Problems are the ones that did not).
	PassedChecks []string  `json:"passed_checks"`
	Error        string    `json:"error,omitempty"` // why the case could not be run to the end
	Steps        int       `json:"steps"`           // total ATIF steps over its trajectories
	CostUSD      float64   `json:"cost_usd"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	// DatasetID is the dataset the case ran in (nil if it was never created); the knowledge graph the case built
	// is the graph of that dataset (see GetExperimentCaseResultDataset).
	DatasetID *int64 `json:"dataset_id,omitempty"`
	// Trajectories are the ATIF trajectories of the case, in call order (remember calls, then the recall).
	Trajectories []ExperimentTrajectory `json:"trajectories,omitempty"`
}

// ExperimentTrajectory references the ATIF trajectory of one remember or recall call of a case. The trajectory
// itself is the JSON file <TrajectoryID>.json (Path, when it was stored) of the trajectory directory.
type ExperimentTrajectory struct {
	ID           int64   `json:"id"`
	CaseResultID int64   `json:"case_result_id"`
	Seq          int     `json:"seq"`           // position within the case, from 1
	Kind         string  `json:"kind"`          // TrajectoryRemember or TrajectoryRecall
	TrajectoryID string  `json:"trajectory_id"` // ATIF trajectory_id
	AgentName    string  `json:"agent_name"`
	Steps        int     `json:"steps"`
	CostUSD      float64 `json:"cost_usd"`
	Path         string  `json:"path,omitempty"` // where the trajectory JSON was stored ("" if it was not)
	// ArtifactIDs are the IDs, in the dataset the case ran in, of the artifacts the call is about: for a remember
	// call the artifact it stored, for a recall the artifacts the answer's evidence was sourced from (provenance).
	ArtifactIDs []int64 `json:"artifact_ids"`
	// Artifacts are those artifacts, read from the dataset when the run is loaded (not stored with the reference).
	Artifacts []ExperimentArtifact `json:"artifacts"`
	CreatedAt time.Time            `json:"created_at"`
}

// ExperimentArtifact is an artifact (see the graph package) of the dataset a case ran in.
type ExperimentArtifact struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	ValidAt   time.Time `json:"valid_at"`
	CreatedAt time.Time `json:"created_at"`
}
