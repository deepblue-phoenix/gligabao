package evals_neo

import "time"

// EvalDef is the definition of an eval, held in the DB: a named collection of cases of one Type. Names are unique. It is
// reusable: any number of ExperimentDefs may include it, and it can be run on its own. Its cases are the CaseDefs whose
// EvalDefID is its ID.
type EvalDef struct {
	ID          EvalDefID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Type        Type      `json:"type"`
	// Tags are free labels, to select and group evals.
	Tags []string `json:"tags,omitempty"`
	// Params are eval-level settings that apply to every case (for example the judge to use), interpreted by the Type.
	Params    Params    `json:"params,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Cases are its cases, in order. Filled by getters only; not stored with the eval.
	Cases []CaseDef `json:"cases,omitempty"`
}

// EvalRun is one execution of an EvalDef: it runs the def's cases (or the selected ones), one CaseRun each, which point back
// here through CaseRun.EvalRunID. It holds no list of its case runs.
type EvalRun struct {
	ID    EvalRunID `json:"id"`
	DefID EvalDefID `json:"def_id"`
	// ExperimentRunID is the experiment run this eval run is part of; nil when the eval was run on its own.
	ExperimentRunID *ExperimentRunID `json:"experiment_run_id,omitempty"`

	Status Status `json:"status"`
	Error  string `json:"error,omitempty"` // why it errored (StatusErrored only)
	// Params are how this run was invoked (selected cases, overrides, ...), merged over the def's Params (and the
	// experiment's) by whoever started it; the result is what is stored.
	Params  Params  `json:"params,omitempty"`
	Counts  Counts  `json:"counts"`
	CostUSD float64 `json:"cost_usd"` // LLM cost of all its case runs
	Timing

	// CaseRuns are its case runs. Filled by getters only; not stored with the run.
	CaseRuns []CaseRun `json:"case_runs,omitempty"`
}
