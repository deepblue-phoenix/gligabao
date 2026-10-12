// Package model holds the types shared by package relational and its backends (sqlite, postgres): the records they
// read and write. It has no dependencies so that every backend can import it. Graph records live in
// internal/db/graph/model.
package model

import (
	"errors"
	"time"
)

// Errors returned by the backends.
var (
	// ErrNotFound means the requested row does not exist (or is not visible, e.g. soft-deleted).
	ErrNotFound = errors.New("not found")
	// ErrExists means a live row with that name already exists.
	ErrExists = errors.New("already exists")
	// ErrRunNotRunning means results were recorded for an experiment run that is already finished.
	ErrRunNotRunning = errors.New("experiment run is not running")
)

//---------------------------------------------------------------------------------------------

// Dataset describes a dataset.
type Dataset struct {
	ID          int64      `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Tags        []string   `json:"tags"`
	CreatedAt   time.Time  `json:"created_at"`
	Deleted     bool       `json:"deleted"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

// Ontology is the metadata of a loaded ontology. The ontology itself (classes and relationships) is stored in the
// graph database (see internal/db/graph).
type Ontology struct {
	ID          int64     `json:"id"`
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	IRI         string    `json:"iri,omitempty"`   // the owl:Ontology IRI declared by the document
	Title       string    `json:"title,omitempty"` // its label
	Format      string    `json:"format"`          // serialization of the loaded document, e.g. "rdfxml"
	SizeBytes   int       `json:"size_bytes"`      // size of the loaded document
	SHA256      string    `json:"sha256"`          // hex digest of the loaded document
	Classes     int       `json:"classes"`
	Properties  int       `json:"properties"` // distinct properties that became edges
	Edges       int       `json:"edges"`      // relationships between classes, subclass_of ones included
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"` // when the ontology was last loaded or replaced; equals created_at until updated
}

// LLMCallStat sums the logged LLM calls of one type.
type LLMCallStat struct {
	Calls        int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

//---------------------------------------------------------------------------------------------
// Eval experiments

// Run and eval statuses.
const (
	// ExperimentRunning is the status of a run that has not been finished yet.
	ExperimentRunning = "running"
	// ExperimentPassed is the status of a finished run whose evals all passed, and of a passed eval.
	ExperimentPassed = "passed"
	// ExperimentFailed is the status of a finished run with at least one eval that did not pass, and of an eval
	// whose checks (temporal facts, expected answer) failed.
	ExperimentFailed = "failed"
	// ExperimentErrored is the status of an eval that could not be run to the end (server, LLM or API error).
	ExperimentErrored = "error"
)

// Trajectory kinds: the operation whose ATIF trajectory it is.
const (
	TrajectoryRemember = "remember"
	TrajectoryRecall   = "recall"
)

// Experiment is a named series of eval runs. Its runs (ExperimentRun) are what was actually executed; Evals lists
// every eval (suite + case name) any of its runs executed. Names are unique.
type Experiment struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Config      map[string]any `json:"config"` // free-form settings under test (models, prompts, ...)
	CreatedAt   time.Time      `json:"created_at"`
	// Evals are the evals the experiment's runs executed, ordered by suite and name. Filled by GetExperiment only.
	Evals []EvalRef `json:"evals,omitempty"`
}

// EvalRef identifies an eval: its case name and the suite (the suite file name without ".json") that holds it.
type EvalRef struct {
	Suite string `json:"suite"`
	Name  string `json:"name"`
}

// ExperimentRun is one execution of an experiment: a pass over some of the evals in the suites.
type ExperimentRun struct {
	ID           int64               `json:"id"`
	ExperimentID int64               `json:"experiment_id"`
	Status       string              `json:"status"`       // ExperimentRunning, ExperimentPassed or ExperimentFailed
	Params       map[string]any      `json:"params"`       // how the run was invoked (suites dir, selected cases, ...)
	TotalEvals   int                 `json:"total_evals"`  // set when the run is finished
	PassedEvals  int                 `json:"passed_evals"` // set when the run is finished
	FailedEvals  int                 `json:"failed_evals"` // failed + errored; set when the run is finished
	CostUSD      float64             `json:"cost_usd"`     // LLM cost of all evals; set when the run is finished
	StartedAt    time.Time           `json:"started_at"`   //
	FinishedAt   *time.Time          `json:"finished_at,omitempty"`
	Evals        []ExperimentRunEval `json:"evals,omitempty"` // filled by GetExperimentRun only
}

// ExperimentRunEval is the result of one eval within a run.
type ExperimentRunEval struct {
	ID       int64    `json:"id"`
	RunID    int64    `json:"run_id"`
	Suite    string   `json:"suite"`
	Name     string   `json:"name"`
	Status   string   `json:"status"` // ExperimentPassed, ExperimentFailed or ExperimentErrored
	Answer   string   `json:"answer,omitempty"`
	Problems []string `json:"problems"` // failed checks
	// PassedChecks are the checks that held (Problems are the ones that did not).
	PassedChecks []string  `json:"passed_checks"`
	Error        string    `json:"error,omitempty"` // why the eval could not be run to the end
	Steps        int       `json:"steps"`           // total ATIF steps over its trajectories
	CostUSD      float64   `json:"cost_usd"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	// HasGraph says that a snapshot of the knowledge graph the eval built was stored (see GetExperimentRunEvalGraph).
	HasGraph bool `json:"has_graph"`
	// Graph is the JSON snapshot to store (RecordExperimentRunEval only; never read back with the eval).
	Graph string `json:"-"`
	// Trajectories are the ATIF trajectories of the eval, in call order (remember calls, then the recall).
	Trajectories []ExperimentTrajectory `json:"trajectories,omitempty"`
}

// ExperimentTrajectory references the ATIF trajectory of one remember or recall call of an eval. The trajectory
// itself is the JSON file <TrajectoryID>.json (Path, when it was stored) of the trajectory directory.
type ExperimentTrajectory struct {
	ID           int64   `json:"id"`
	RunEvalID    int64   `json:"run_eval_id"`
	Seq          int     `json:"seq"`           // position within the eval, from 1
	Kind         string  `json:"kind"`          // TrajectoryRemember or TrajectoryRecall
	TrajectoryID string  `json:"trajectory_id"` // ATIF trajectory_id
	AgentName    string  `json:"agent_name"`
	Steps        int     `json:"steps"`
	CostUSD      float64 `json:"cost_usd"`
	Path         string  `json:"path,omitempty"` // where the trajectory JSON was stored ("" if it was not)
	// Artifacts are the artifacts the call is about, as they were when the eval ran: for a remember call the input
	// artifact the caller gave, for a recall the artifacts the answer's evidence was sourced from (provenance).
	Artifacts []ExperimentArtifact `json:"artifacts"`
	CreatedAt time.Time            `json:"created_at"`
}

// ExperimentArtifact is a snapshot of an artifact (see the graph package) taken while an eval ran, because the
// eval database is reset between runs.
type ExperimentArtifact struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	ValidAt   time.Time `json:"valid_at"`
	CreatedAt time.Time `json:"created_at"`
}
