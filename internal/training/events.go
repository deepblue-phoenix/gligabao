package training

// The training events of the jobs (monitoring.TrainingEvent) as the training API and the monitor make them: the events of the ML
// sidecar's log of a job, completed with the job they are of, and the events of gligabao itself (started, stop requested, model
// recorded, stream error). The ML sidecar has one backend today, Modal, so every event says so; another backend will say its own
// provider type here.

import (
	"time"

	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
)

// providerOf is the compute provider that runs the jobs of a model type: the ML sidecar's backend (Modal) for the LoRA jobs, sargo for
// the GCNII* ones.
func providerOf(modelType string) string {
	if modelType == trainingdb.ModelTypeGCNII {
		return monitoring.ProviderSargo
	}
	return monitoring.ProviderModal
}

// ofRun is the event ev of the ML sidecar's log of the job run, completed with the job it is of (ev is not changed).
func ofRun(run trainingdb.TrainingJobRun,
	ev monitoring.TrainingEvent) monitoring.TrainingEvent {
	ev.RunID = run.ID
	ev.JobID = run.JobID
	ev.UserID = run.UserID
	ev.ProviderType = providerOf(run.Type())
	return ev
}

// jobEvent is a new event of gligabao itself of the type typ (monitoring.EventStarted, ...) about the job run: live only (monitoring.NoSeq),
// stamped now. The caller sets what else the type has.
func jobEvent(run trainingdb.TrainingJobRun,
	typ string) monitoring.TrainingEvent {
	return ofRun(run, monitoring.TrainingEvent{
		Seq:       monitoring.NoSeq,
		Type:      typ,
		Timestamp: float64(time.Now().UnixNano()) / 1e9,
	})
}
