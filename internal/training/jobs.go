package training

// Starting and stopping training jobs on their backends (backend.go), and reading the events a job reported (the Monitor stores them).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/ml_sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

// StartJobRequest is what the caller of StartJob chooses.
type StartJobRequest struct {
	// UserID is the user the job is for (never empty): the run and the model it makes are theirs.
	UserID string
	// ModelType is what to train ("" is LoRA), and so which backend runs the job.
	ModelType string
	// Params is the body of the request, as the backend of the model type reads it (LoRAParams for LoRA: dataset, num_train_steps,
	// model_size; for GCNII* see internal/training/gcnii).
	Params json.RawMessage
}

// StartJob starts a training job of in.ModelType on its backend and stores the run in the gb_training_job_runs table of the database, then
// has mon (may be nil) follow the job's progress. It returns the stored record. Errors are *Error: KindInvalid for a bad request,
// KindUnavailable for a model type whose backend is not on, KindConflict for a user that is training already (GCNII*), KindUpstream for
// a backend service error, KindInternal.
func StartJob(ctx context.Context,
	db *sql.DB,
	backends Backends,
	mon *Monitor,
	in StartJobRequest) (*trainingdb.TrainingJobRun, error) {
	if strings.TrimSpace(in.UserID) == "" {
		return nil, invalidErr("a training job needs a user")
	}
	b, err := backends.Of(in.ModelType)
	if err != nil {
		return nil, err
	}
	if err := b.Reap(ctx, in.UserID); err != nil {
		log.WithError(err).WithField("user_id", in.UserID).Warn("training: reaping the runs of a stopped instance failed")
	}
	run, err := b.Start(ctx,
		StartInput{
			UserID: in.UserID,
			Params: in.Params,
			Record: func(ctx context.Context,
				r trainingdb.TrainingJobRun) (*trainingdb.TrainingJobRun, error) {
				return recordRun(ctx,
					db,
					b.ModelType(),
					r)
			},
			Abort: func(ctx context.Context,
				r trainingdb.TrainingJobRun,
				cause error) {
				abortRun(ctx,
					db,
					r,
					cause)
			},
		})
	if err != nil {
		return nil, err
	}
	started := jobEvent(*run, monitoring.EventStarted)
	started.MaxSteps = &run.NumTrainSteps
	mon.Emit(started)
	mon.Watch(*run)
	return run, nil
}

// recordRun stores a run of the model type and reads it back. A GCNII* run of a user that has an unfinished one is refused (KindConflict).
func recordRun(ctx context.Context,
	db *sql.DB,
	modelType string,
	r trainingdb.TrainingJobRun) (*trainingdb.TrainingJobRun, error) {
	r.ModelType = modelType
	id, err := trainingdb.RecordTrainingJobRun(ctx,
		db,
		r)
	if err != nil {
		return nil, err
	}
	return trainingdb.GetTrainingJobRun(ctx,
		db,
		id)
}

// abortRun marks a run that its service refused to start as failed, with the cause.
func abortRun(ctx context.Context,
	db *sql.DB,
	r trainingdb.TrainingJobRun,
	cause error) {
	now := time.Now().UTC()
	if err := trainingdb.UpdateTrainingJobRun(context.WithoutCancel(ctx),
		db,
		r.ID,
		trainingdb.TrainingJobRunUpdate{
			Status:     trainingdb.TrainingJobFailed,
			Error:      cause.Error(),
			FinishedAt: &now,
		}); err != nil {
		log.WithError(err).WithField("run_id", r.ID).Error("training: marking a refused run failed failed")
	}
}

// GetJobRun reads training job run id of the database, which must be a job of userID (the run of another user is as unknown as one that
// does not exist): a KindNotFound or KindInternal Error if it cannot.
func GetJobRun(ctx context.Context,
	db *sql.DB,
	userID string,
	id int64) (*trainingdb.TrainingJobRun, error) {
	run, err := trainingdb.GetTrainingJobRun(ctx,
		db,
		id)
	if errors.Is(err, relational.ErrNotFound) || (err == nil && run.UserID != userID) {
		return nil, notFoundErr("unknown training job run")
	} else if err != nil {
		log.WithError(err).Error("training: read training job run failed")
		return nil, internalErr("failed to read the training job run", err)
	}
	return run, nil
}

// StopJob asks the backend of the job run id to stop it. The job ends at its next step (or at the next stage of its setup) with a
// "cancelled" event, which the monitor records like any other: the job's row changes when the event arrives. It returns the
// run. Errors are *Error: KindNotFound for an unknown run, KindConflict for one that is over already (or runs on another instance),
// KindUnavailable for a backend that is not on, KindUpstream for a backend service error.
func StopJob(ctx context.Context,
	db *sql.DB,
	backends Backends,
	mon *Monitor,
	userID string,
	id int64) (*trainingdb.TrainingJobRun, error) {
	run, err := GetJobRun(ctx,
		db,
		userID,
		id)
	if err != nil {
		return nil, err
	}
	if trainingdb.TrainingJobFinished(run.Status) {
		return nil, conflictErr("the training job is over already (" + run.Status + ")")
	}
	b, err := backends.Of(run.Type())
	if err != nil {
		return nil, err
	}
	if err := b.Cancel(ctx, *run); err != nil {
		return nil, err
	}
	log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID}).Info("training: stop requested")
	mon.Emit(jobEvent(*run, monitoring.EventStopRequested))
	return run, nil
}

// JobHistory returns the events already stored of job run runID of the database, from sequence number from on, as the monitor feeds them.
// Errors are *Error: KindNotFound for an unknown run, KindInternal.
func JobHistory(ctx context.Context,
	db *sql.DB,
	userID string,
	runID int64,
	from int) ([]monitoring.TrainingEvent, error) {
	run, err := GetJobRun(ctx,
		db,
		userID,
		runID)
	if err != nil {
		return nil, err
	}
	history, err := trainingdb.ListTrainingJobEvents(ctx,
		db,
		runID,
		from)
	if err != nil {
		log.WithError(err).Error("training: read training job events failed")
		return nil, internalErr("failed to read the training job events", err)
	}
	out := make([]monitoring.TrainingEvent,
		0,
		len(history))
	for _, e := range history {
		var ev ml_sidecar.TrainingEvent
		if json.Unmarshal([]byte(e.Data), &ev) != nil {
			continue
		}
		ev.Seq = e.Seq
		out = append(out, ofRun(*run, ev))
	}
	return out, nil
}
