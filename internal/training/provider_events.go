package training

// The provider events of the training jobs (monitoring.ProviderEvent): what the compute provider behind the ML sidecar does for
// a job, as the training API and the monitor see it. The ML sidecar has one backend today, Modal, so every event says so;
// another backend will say its own provider type here.

import (
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/ml_sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
)

// providerType is the provider of the ML sidecar's backend.
const providerType = monitoring.ProviderModal

// Types of the provider events of a training job.
const (
	ProviderEventJobStarted       = "training_job.started"        // lifecycle: the provider accepted the job
	ProviderEventJobStopRequested = "training_job.stop_requested" // lifecycle: a stop was asked of the provider
	ProviderEventJobStatus        = "training_job.status"         // lifecycle: running, training, completed, failed, cancelled
	ProviderEventJobProgress      = "training_job.progress"       // progress: a step with its loss
	ProviderEventJobLog           = "training_job.log"            // log: what the job is doing
	ProviderEventModelRecorded    = "training_job.model_recorded" // lifecycle: the weights of a completed job were recorded
	ProviderEventStreamError      = "training_job.stream_error"   // lifecycle: the event stream of the job failed (it is retried)
)

// jobFields are the fields every provider event of a run has.
func jobFields(run trainingdb.TrainingJobRun) monitoring.Event {
	return monitoring.Event{
		"run_id":          run.ID,
		"job_id":          run.JobID,
		"provider_job_id": run.ProviderJobID,
		"dataset":         run.Dataset,
		"model_size":      run.ModelSize,
	}
}

// withFields is data plus extra (neither is changed).
func withFields(data monitoring.Event,
	extra monitoring.Event) monitoring.Event {
	out := make(monitoring.Event, len(data)+len(extra))
	for k, v := range data {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func jobEvent(typ,
	kind string,
	run trainingdb.TrainingJobRun,
	extra monitoring.Event) monitoring.ProviderEvent {
	return monitoring.ProviderEvent{
		ProviderType: providerType,
		Type:         typ,
		Kind:         kind,
		UserID:       run.UserID,
		Data:         withFields(jobFields(run), extra),
	}
}

// trainingEventToProviderEvent is the provider event of an event of the ML sidecar's stream for the run.
func trainingEventToProviderEvent(run trainingdb.TrainingJobRun,
	ev ml_sidecar.TrainingEvent) monitoring.ProviderEvent {
	extra := monitoring.Event{"seq": ev.Seq}
	typ, kind := ProviderEventJobLog, monitoring.KindLog
	switch ev.Type {
	case ml_sidecar.EventStatus:
		typ, kind = ProviderEventJobStatus, monitoring.KindLifecycle
		extra["status"] = ev.Status
		if ev.MaxSteps != nil {
			extra["max_steps"] = *ev.MaxSteps
		}
		if ev.FinalLoss != nil {
			extra["final_loss"] = *ev.FinalLoss
		}
		if ev.Error != "" {
			extra["error"] = ev.Error
		}
	case ml_sidecar.EventProgress:
		typ, kind = ProviderEventJobProgress, monitoring.KindProgress
		if ev.Step != nil {
			extra["step"] = *ev.Step
		}
		if ev.Loss != nil {
			extra["loss"] = *ev.Loss
		}
		if ev.LearningRate != nil {
			extra["learning_rate"] = *ev.LearningRate
		}
		if ev.Epoch != nil {
			extra["epoch"] = *ev.Epoch
		}
		if ev.ElapsedSeconds != nil {
			extra["elapsed_seconds"] = *ev.ElapsedSeconds
		}
	default:
		extra["message"] = ev.Message
	}
	out := jobEvent(typ,
		kind,
		run,
		extra)
	if ev.Timestamp > 0 {
		sec := int64(ev.Timestamp)
		out.Time = time.Unix(sec, int64((ev.Timestamp-float64(sec))*1e9)).UTC()
	}
	return out
}
