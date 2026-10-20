package core

import (
	"encoding/json"
	"time"
)

// IDs are one type per kind of record, so that an EvalDefID cannot be passed where an EvalRunID is expected.
type (
	ExperimentDefID int64
	ExperimentRunID int64
	EvalDefID       int64
	EvalRunID       int64
	CaseDefID       int64
	CaseRunID       int64
)

// Type is the kind of an eval ("knowledge", "training", ...). It says how the Input, Expected, Output and Details of its
// cases and case runs are to be read, and which runner executes them. An EvalDef has one Type and all its cases share it.
type Type string

// Status is the lifecycle of a run (ExperimentRun, EvalRun, CaseRun). It says whether the run ran to the end, not how well
// it went: for that see Verdict.
type Status string

const (
	// StatusPending: created, not started.
	StatusPending Status = "pending"
	// StatusRunning: started, not finished.
	StatusRunning Status = "running"
	// StatusCompleted: ran to the end. Says nothing about the verdict.
	StatusCompleted Status = "completed"
	// StatusErrored: could not be run to the end (server, LLM or API error). Error holds why.
	StatusErrored Status = "errored"
	// StatusCancelled: stopped on request before it ended.
	StatusCancelled Status = "cancelled"
)

// Finished reports whether the status is a final one.
func (s Status) Finished() bool {
	return s == StatusCompleted || s == StatusErrored || s == StatusCancelled
}

// Verdict is the judgement of a COMPLETED case run: did it meet its expectations. It is empty (VerdictNone) for a case
// that is scored only, or not completed. Eval and experiment runs do not have one: they have Counts.
type Verdict string

const (
	VerdictNone Verdict = ""
	VerdictPass Verdict = "pass"
	VerdictFail Verdict = "fail"
)

// Counts summarizes the case runs of an EvalRun (or of all the eval runs of an ExperimentRun). Every case run is counted in
// exactly one of Pending, Running, Errored, Cancelled or Completed; Passed and Failed split the completed ones that have a
// verdict (a completed case with VerdictNone is in neither). Total is the number of cases the run is to execute.
type Counts struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"` // not reached yet (a cancelled run leaves some)
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Errored   int `json:"errored"`
	Cancelled int `json:"cancelled"`
	Passed    int `json:"passed"`
	Failed    int `json:"failed"`
}

// Timing is when a run happened. FinishedAt is nil until the run's status is final.
type Timing struct {
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Params are free-form settings. On a def they are the settings under test (models, prompts, judge, ...); on a run they are
// how that run was invoked (what was selected, overrides, ...).
type Params map[string]any

// Payload is type-specific JSON, opaque to this package. Its shape is defined by the Type of the eval.
type Payload = json.RawMessage
