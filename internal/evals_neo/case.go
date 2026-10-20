package evals_neo

import "time"

// CaseDef is the definition of a case, held in the DB: one input, what is expected of it, and the parameters of the check.
// A case belongs to exactly one EvalDef; its Name is unique within that eval. What Input, Expected and Params contain
// depends on the Type of the eval, which also interprets them.
type CaseDef struct {
	ID        CaseDefID `json:"id"`
	EvalDefID EvalDefID `json:"eval_def_id"`
	Name      string    `json:"name"`
	// Position orders the case within its eval, from 0.
	Position    int    `json:"position"`
	Description string `json:"description,omitempty"`
	// Tags are free labels, to select and group cases.
	Tags []string `json:"tags,omitempty"`
	// Input is what is given to the system under test.
	Input Payload `json:"input,omitempty"`
	// Expected is what a correct outcome is: a reference answer, facts to find, strings to contain, ... It may be empty
	// (a case that is only scored, or only observed).
	Expected Payload `json:"expected,omitempty"`
	// Params are the settings of this case (limits, which checks to apply, ...), over the eval's Params.
	Params    Params    `json:"params,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CaseRun is one execution of a CaseDef within an EvalRun: what was given, what came back, and how it went.
type CaseRun struct {
	ID        CaseRunID `json:"id"`
	DefID     CaseDefID `json:"def_id"`
	EvalRunID EvalRunID `json:"eval_run_id"`

	// Status is the lifecycle: did it run to the end. Error says why not (StatusErrored only).
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`

	// Input is what was actually given (the def's Input, resolved if the Type does so), and Output what came back. The
	// def may change after the run; these stay as they were.
	Input  Payload `json:"input,omitempty"`
	Output Payload `json:"output,omitempty"`

	// The outcome of a COMPLETED run; zero when it did not complete.
	// Verdict is pass or fail against Expected (VerdictNone if the case is only scored).
	Verdict Verdict `json:"verdict,omitempty"`
	// Score is a number in [0, 1] (nil if the case has none); the Type says how it is computed.
	Score *float64 `json:"score,omitempty"`
	// Details is the type-specific result: the checks that held and the ones that did not, the judge's reasoning, the
	// references to trajectories, ...
	Details Payload `json:"details,omitempty"`

	CostUSD float64 `json:"cost_usd"`
	Timing
}
