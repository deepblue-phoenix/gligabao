package core

import "errors"

// RunOptions say how a user eval run is started (see runmanager.Manager.Start and POST /v1/gb/evals/{name}/runs).
type RunOptions struct {
	// Cases are the cases to run (all if empty).
	Cases []string
	// Models and Reasoning are the model and the reasoning level per stage (a case's own win).
	Models, Reasoning map[string]string
	// Geo is whether geo is on (default true; off if the server has no geo resolver).
	Geo *bool
}

var (
	// ErrTooManyRuns means the user (or the server) has the most runs allowed in progress (HTTP 429, "too many runs in progress").
	ErrTooManyRuns = errors.New("too many runs in progress")
	// ErrDailyCap means the user has reached the cost cap of the day (HTTP 429).
	ErrDailyCap = errors.New("the daily cost cap of evals is reached")
	// ErrNoLLM means the server has no LLM (HTTP 503).
	ErrNoLLM = errors.New("LLM not configured (set ANTHROPIC_API_KEY)")
	// ErrBadOptions is the error of run options that are not valid (HTTP 400); it wraps the reason.
	ErrBadOptions = errors.New("invalid run options")
)
