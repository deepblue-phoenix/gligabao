// Package model holds the types shared by package relational and its backends (sqlite, postgres): the records they
// read and write. It has no dependencies so that every backend can import it. Graph records live in
// internal/db/graph/model.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
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

// Inference is one invocation of a model (gb_inferences): the dynamic counterpart of Model, which is the static
// description of a model. Every LLM call, embedding request, web search and Jev classification the system makes is
// recorded as one, with what that invocation used and cost.
type Inference struct {
	ID int64 `json:"id"`
	// Type is the operation that made the call: "remember", "remember_image" or "recall".
	Type string `json:"type"`
	// Stage is the pipeline stage that made it (e.g. "extract_graph", "summarize_chunk", "recall", "embed"); "" if unknown.
	Stage string `json:"stage,omitempty"`
	// ModelID is the ID (gb_models) of the model that was run, "" if the API did not say. On RecordInference it is filled
	// in from Model.
	ModelID string `json:"model_id,omitempty"`
	// Model, Provider and Kind describe the model. RecordInference uses them to register it if it is not stored yet (Kind
	// "" means "llm"); reading a run fills them from gb_models.
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Kind     string `json:"kind,omitempty"`
	// Reasoning is the thinking level the model ran with: "none", "low", "medium" or "high". "" is stored as "none".
	Reasoning    string    `json:"reasoning"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"` // includes the thinking tokens
	CostUSD      float64   `json:"cost_usd"`      // 0 if the model's price is unknown
	CreatedAt    time.Time `json:"created_at"`
}

// InferenceStat sums the recorded inferences of one type.
type InferenceStat struct {
	Inferences   int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// InferenceFilter selects inferences; an empty field matches all. ModelID is gb_models.id.
type InferenceFilter struct {
	Type      string
	Stage     string
	ModelID   string
	Reasoning string
}

// InferenceGroup sums the inferences that share a type, a stage, a model and a thinking level. Model is the model's name
// and ModelID its ID, "" for runs without one.
type InferenceGroup struct {
	Type         string  `json:"type"`
	Stage        string  `json:"stage,omitempty"`
	ModelID      string  `json:"model_id,omitempty"`
	Model        string  `json:"model,omitempty"`
	Reasoning    string  `json:"reasoning"`
	Inferences   int64   `json:"inferences"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
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
	// DatasetID is the dataset the eval ran in (nil if it was never created); the knowledge graph the eval built
	// is the graph of that dataset (see GetExperimentRunEvalDataset).
	DatasetID *int64 `json:"dataset_id,omitempty"`
	// Trajectories are the ATIF trajectories of the eval, in call order (remember calls, then the recall).
	Trajectories []ExperimentTrajectory `json:"trajectories,omitempty"`
}

// ExperimentEvalPoint is the result of one eval in one run of an experiment: a point of the eval's history.
type ExperimentEvalPoint struct {
	RunID        int64     `json:"run_id"`
	RunEvalID    int64     `json:"run_eval_id"`
	RunStatus    string    `json:"run_status"`
	RunStartedAt time.Time `json:"run_started_at"`
	Status       string    `json:"status"`        // ExperimentPassed, ExperimentFailed or ExperimentErrored
	Problems     []string  `json:"problems"`      // failed checks
	PassedChecks []string  `json:"passed_checks"` // checks that held
	Error        string    `json:"error,omitempty"`
	Steps        int       `json:"steps"`
	CostUSD      float64   `json:"cost_usd"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
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
	// ArtifactIDs are the IDs, in the dataset the eval ran in, of the artifacts the call is about: for a remember
	// call the artifact it stored, for a recall the artifacts the answer's evidence was sourced from (provenance).
	ArtifactIDs []int64 `json:"artifact_ids"`
	// Artifacts are those artifacts, read from the dataset when the run is loaded (not stored with the reference).
	Artifacts []ExperimentArtifact `json:"artifacts"`
	CreatedAt time.Time            `json:"created_at"`
}

// ExperimentArtifact is an artifact (see the graph package) of the dataset an eval ran in.
type ExperimentArtifact struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	ValidAt   time.Time `json:"valid_at"`
	CreatedAt time.Time `json:"created_at"`
}

//---------------------------------------------------------------------------------------------

// Model is an AI model (gb_models). Entities and edges of the graph reference the model that inferred them by ID.
type Model struct {
	// ID is derived from Name alone (see ModelID), so the same model has the same ID in every database.
	ID string `json:"id"`
	// Name is the model name as the provider's API reports it (or, for a supported family priced by prefix, the
	// price-table key, e.g. "claude-haiku-4-5").
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"` // "anthropic", "openai", ... ("" if unknown)
	Kind     string `json:"kind"`               // "llm" or "embedding"
	// Supported is true for the models gligabao supports at its last start (they are re-inserted on every startup),
	// false for a model that was only seen in an API response (e.g. a dated snapshot of a supported family).
	Supported bool      `json:"supported"`
	CreatedAt time.Time `json:"created_at"`
}

// ModelID returns the ID of the model called name: the first 8 bytes of the SHA-256 of the name (trimmed and
// lowercased), in hex. It is deterministic, so every database gives a model the same ID, and "" for an empty name.
func ModelID(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:8])
}
