package core

import "time"

// ExperimentDef is the definition of an experiment, held in the DB: a named group of evals that are run together, plus the
// settings under test. Names are unique. It does not say what happened (see ExperimentRun).
type ExperimentDef struct {
	ID          ExperimentDefID `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	// EvalDefIDs are the evals that make up the experiment, in the order they run. An EvalDef may be in many experiments.
	EvalDefIDs []EvalDefID `json:"eval_def_ids"`
	// Params are the settings under test (models, prompts, judge, ...), for every eval run of the experiment.
	Params    Params    `json:"params,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExperimentRun is one execution of an ExperimentDef: it runs each of the def's evals once (one EvalRun each, which point
// back here through EvalRun.ExperimentRunID). It holds no list of its eval runs.
type ExperimentRun struct {
	ID    ExperimentRunID `json:"id"`
	DefID ExperimentDefID `json:"def_id"`

	Status Status `json:"status"`
	Error  string `json:"error,omitempty"` // why it errored (StatusErrored only)
	// Params are how this run was invoked (selected evals, overrides of the def's Params, ...).
	Params Params `json:"params,omitempty"`
	// Counts are over the case runs of all its eval runs. CostUSD is the LLM cost of all of them.
	Counts  Counts  `json:"counts"`
	CostUSD float64 `json:"cost_usd"`
	Timing

	// EvalRuns are its eval runs. Filled by getters only; not stored with the run.
	EvalRuns []EvalRun `json:"eval_runs,omitempty"`
}
