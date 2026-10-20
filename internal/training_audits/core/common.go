package core

import "time"

// IDs are one type per kind of record, so that an AuditRunID cannot be passed where an AuditCaseRunID is expected.
type (
	AuditRunID     int64
	AuditCaseRunID int64
)

// Status is the lifecycle of a run (AuditRun, AuditCaseRun). It says whether the run ran to the end, not how well it went.
type Status string

const (
	// StatusPending: created, not started (a case the run has not reached yet).
	StatusPending Status = "pending"
	// StatusRunning: started, not finished.
	StatusRunning Status = "running"
	// StatusCompleted: ran to the end: the model answered and the judge scored the answer (a score of 0 too).
	StatusCompleted Status = "completed"
	// StatusErrored: could not be run to the end (the model, the judge or the server failed). Error holds why.
	StatusErrored Status = "errored"
	// StatusCancelled: stopped on request before it ended; what was done is kept.
	StatusCancelled Status = "cancelled"
)

// Finished reports whether the status is a final one.
func (s Status) Finished() bool {
	return s == StatusCompleted || s == StatusErrored || s == StatusCancelled
}

// Counts summarizes the case runs of an AuditRun. Every example is counted in exactly one of Pending, Running, Errored, Cancelled
// or Completed. Total is the number of examples the run is to ask.
type Counts struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"` // not reached yet (a cancelled run leaves some)
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Errored   int `json:"errored"`
	Cancelled int `json:"cancelled"`
}

// Timing is when a run happened. FinishedAt is nil until the run's status is final.
type Timing struct {
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Params are free-form settings: how a run was invoked (batch size, max tokens, temperature, limit, ...).
type Params map[string]any
