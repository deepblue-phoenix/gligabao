package training

// The training pipeline is one for every kind of model: the same start, stop, follow, record and feed code, and the same HTTP handlers
// (api/training). What differs between the kinds is where the job runs, and a Backend is that difference: the LoRA fine-tuning runs on the ML
// sidecar (MLSidecarBackend, backend_mlsidecar.go), the GCNII* graph classifier on sargo (internal/training/gcnii). A backend hides its
// service: it starts a job, streams its events in the one event shape (monitoring.TrainingEvent), stops it and says what model a completed
// job made. The Monitor keeps the run's row, stores the events and feeds them to the SSE stream, whatever the backend.

import (
	"context"
	"encoding/json"

	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
)

// StartInput is what a backend gets to start a job.
type StartInput struct {
	// UserID is the user the job is for (never empty).
	UserID string
	// Params is the body of the request that starts the job (the fields depend on the model type; a backend checks its own).
	Params json.RawMessage
	// Record stores the run (the row of gb_training_job_runs) and returns it as stored, with its ID. A backend calls it once: LoRA after
	// the ML sidecar has accepted the job (its job ID is the sidecar's), GCNII* before sargo is asked, with the job ID it chose, so
	// that the row is what keeps a second training of the same user (on any instance) out. The ModelType of the row is set by Record.
	Record func(ctx context.Context,
		run trainingdb.TrainingJobRun) (*trainingdb.TrainingJobRun, error)
	// Abort ends a run that Record stored and the service then refused to start: it is marked failed with the cause.
	Abort func(ctx context.Context,
		run trainingdb.TrainingJobRun,
		cause error)
}

// ModelRecord is the model a completed job made, to be recorded as a trained model.
type ModelRecord struct {
	Model trainingdb.TrainedModel
	// Info is the model as the "model_recorded" event reports it (nil: the event has none).
	Info *monitoring.TrainedModelInfo
	// Done, if set, is called with the ID of the recorded model (a backend that has more to do once the row exists).
	Done func(ctx context.Context,
		modelID int64) error
}

// Backend is where one kind of training job runs. Its methods are safe for concurrent use.
type Backend interface {
	// ModelType is the kind of model it trains (trainingdb.ModelTypeLoRA, trainingdb.ModelTypeGCNII): the key it is found by.
	ModelType() string
	// Start checks the request and starts the job (see StartInput). Errors are *Error: KindInvalid for a bad request, KindConflict for
	// a user that is training already, KindUpstream for a service that failed, KindInternal.
	Start(ctx context.Context,
		in StartInput) (*trainingdb.TrainingJobRun, error)
	// Owns says whether this process is the one that follows the run (false: another instance of a cluster does, or nobody does).
	Owns(run trainingdb.TrainingJobRun) bool
	// Stream calls fn with the events of the run from the one with sequence number from on, until the job ends (it returns nil after
	// the event that ends it), fn fails (its error is returned), or ctx is done. An error of the service is returned too: the monitor
	// calls Stream again.
	Stream(ctx context.Context,
		run trainingdb.TrainingJobRun,
		from int,
		fn func(monitoring.TrainingEvent) error) error
	// Cancel asks for the run to stop: the job ends with a "cancelled" event. Errors are *Error: KindConflict for a job that is over
	// already or runs on another instance, KindUpstream.
	Cancel(ctx context.Context,
		run trainingdb.TrainingJobRun) error
	// Model is the model a completed job made, from its "completed" event (nil: it made none, or the service did not report it).
	Model(ctx context.Context,
		run trainingdb.TrainingJobRun,
		ev monitoring.TrainingEvent) (*ModelRecord, error)
	// Ended is called once the event that ends the run was stored (modelID is the recorded model, 0 if none).
	Ended(ctx context.Context,
		run trainingdb.TrainingJobRun,
		ev monitoring.TrainingEvent,
		modelID int64)
	// Reap ends the runs of userID that nobody follows any more (their instance stopped): it marks them failed in the database.
	Reap(ctx context.Context,
		userID string) error
}

// Backends are the backends of a server, by model type.
type Backends map[string]Backend

// NewBackends is the backends bs, by their model type (a nil one is left out).
func NewBackends(bs ...Backend) Backends {
	out := Backends{}
	for _, b := range bs {
		if b != nil {
			out[b.ModelType()] = b
		}
	}
	return out
}

// Of is the backend of the model type ("" is LoRA), or a KindUnavailable / KindInvalid *Error if there is none.
func (bs Backends) Of(modelType string) (Backend, error) {
	if modelType == "" {
		modelType = trainingdb.ModelTypeLoRA
	}
	if b, ok := bs[modelType]; ok {
		return b, nil
	}
	switch modelType {
	case trainingdb.ModelTypeLoRA:
		return nil, &Error{Kind: KindUnavailable, Msg: "ML sidecar is not active"}
	case trainingdb.ModelTypeGCNII:
		return nil, &Error{Kind: KindUnavailable, Msg: "sargo is not active"}
	}
	return nil, invalidErr(`model_type must be "` + trainingdb.ModelTypeLoRA + `" or "` + trainingdb.ModelTypeGCNII + `"`)
}
