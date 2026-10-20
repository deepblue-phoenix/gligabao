package training

// Starting and stopping training jobs on the sidecar, and reading the events a job reported (the Monitor stores them).

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

// JobStarter starts training jobs on the sidecar; *sidecar.Client is one.
type JobStarter interface {
	StartTrainingJob(ctx context.Context, req sidecar.StartTrainingJobRequest) (sidecar.TrainingJob, error)
}

// JobCanceller asks the sidecar to stop training jobs; *sidecar.Client is one.
type JobCanceller interface {
	CancelTrainingJob(ctx context.Context, jobID string) error
}

// StartJobRequest is what the caller of StartJob chooses; the dataset's content is added from the dataset's file.
type StartJobRequest struct {
	NumTrainSteps int
	ModelSize     string
	// Dataset is the name of a dataset of the training datasets directory (manifest.go); the data file is sent to the sidecar in
	// the request that starts the job (the sidecar has no dataset endpoints).
	Dataset string
}

// StartJob starts a training job on the sidecar through sc (the sidecar's REST API) and stores the TrainingJob the sidecar
// reports in the gb_training_job_runs table of the database, then has mon (may be nil) follow the job's progress. It returns the stored
// record. Errors are *Error: KindInvalid for a bad request (also one the sidecar rejects as invalid, with its message), KindUpstream
// for a sidecar error, KindInternal.
func StartJob(ctx context.Context, db *sql.DB, dir string, sc JobStarter, mon *Monitor, in StartJobRequest) (*trainingdb.TrainingJobRun, error) {
	req := sidecar.StartTrainingJobRequest{NumTrainSteps: in.NumTrainSteps, ModelSize: strings.ToLower(strings.TrimSpace(in.ModelSize))}
	name := strings.TrimSpace(in.Dataset)
	if name == "" {
		return nil, invalidErr("dataset is required: the name of a training dataset")
	}
	if req.NumTrainSteps < 0 {
		return nil, invalidErr("num_train_steps must be positive")
	}
	switch req.ModelSize {
	case "", sidecar.ModelSize4B, sidecar.ModelSize12B, sidecar.ModelSize31B:
	default:
		return nil, invalidErr("model_size must be \"4b\", \"12b\" or \"31b\"")
	}

	dataset, content, err := LoadTrainingDataset(ctx, db, dir, name)
	if errors.Is(err, ErrUnknownDataset) {
		return nil, invalidErr("unknown dataset " + name + ": there is no dataset of that name in the training datasets directory")
	} else if err != nil {
		return nil, internalErr(err.Error(), err)
	}
	req.Dataset, req.DatasetContent = dataset.Filename, base64.StdEncoding.EncodeToString(content)
	job, err := sc.StartTrainingJob(ctx, req)
	if err != nil {
		var se *sidecar.StatusError
		if errors.As(err, &se) && se.Code == http.StatusBadRequest {
			return nil, &Error{KindInvalid, se.Message(), err}
		}
		log.WithError(err).Error("training: start training job failed")
		return nil, &Error{KindUpstream, "sidecar failed to start the training job", err}
	}
	// The job is running now: record it even if the caller went away meanwhile.
	ctx = context.WithoutCancel(ctx)
	// the sidecar has taken this version of the file
	if _, err := trainingdb.MarkTrainingDatasetSynced(ctx, db, dataset.ID, dataset.SHA256); err != nil {
		log.WithError(err).WithField("filename", dataset.Filename).Error("training: mark training dataset synced failed")
	}
	id, err := trainingdb.RecordTrainingJobRun(ctx, db, trainingdb.TrainingJobRun{
		JobID: job.JobID, ProviderJobID: job.ProviderJobID, Status: job.Status, ModelSize: job.ModelSize, Dataset: job.Dataset,
		NumTrainSteps: job.NumTrainSteps, StartedAt: job.StartedAt})
	if err != nil {
		log.WithError(err).WithField("job_id", job.JobID).Error("training: record training job run failed")
		return nil, internalErr("training job "+job.JobID+" started but could not be recorded", err)
	}
	run, err := trainingdb.GetTrainingJobRun(ctx, db, id)
	if err != nil {
		log.WithError(err).WithField("job_id", job.JobID).Error("training: read training job run failed")
		return nil, internalErr("failed to read the training job run", err)
	}
	mon.Watch(*run)
	return run, nil
}

// GetJobRun reads training job run id of the database: a KindNotFound or KindInternal Error if it cannot.
func GetJobRun(ctx context.Context, db *sql.DB, id int64) (*trainingdb.TrainingJobRun, error) {
	run, err := trainingdb.GetTrainingJobRun(ctx, db, id)
	if errors.Is(err, relational.ErrNotFound) {
		return nil, notFoundErr("unknown training job run")
	} else if err != nil {
		log.WithError(err).Error("training: read training job run failed")
		return nil, internalErr("failed to read the training job run", err)
	}
	return run, nil
}

// StopJob asks the sidecar to stop the job run id. The job ends at its next step (or at the next stage of its setup) with a
// "cancelled" event, which the monitor records like any other: the job's row changes when the event arrives. It returns the
// run. Errors are *Error: KindNotFound for an unknown run, KindConflict for one that is over already, KindUpstream for a
// sidecar error.
func StopJob(ctx context.Context, db *sql.DB, sc JobCanceller, id int64) (*trainingdb.TrainingJobRun, error) {
	run, err := GetJobRun(ctx, db, id)
	if err != nil {
		return nil, err
	}
	if trainingdb.TrainingJobFinished(run.Status) {
		return nil, conflictErr("the training job is over already (" + run.Status + ")")
	}
	if err := sc.CancelTrainingJob(ctx, run.JobID); err != nil {
		var se *sidecar.StatusError
		if errors.As(err, &se) && se.Code == http.StatusConflict {
			return nil, &Error{KindConflict, se.Message(), err}
		}
		log.WithError(err).WithField("job_id", run.JobID).Error("training: stop training job failed")
		return nil, &Error{KindUpstream, "sidecar failed to stop the training job", err}
	}
	log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID}).Info("training: stop requested")
	return run, nil
}

// JobHistory returns the events already stored of job run runID of the database, from sequence number from on, as the monitor feeds them.
// Errors are *Error: KindNotFound for an unknown run, KindInternal.
func JobHistory(ctx context.Context, db *sql.DB, runID int64, from int) ([]FeedEvent, error) {
	run, err := GetJobRun(ctx, db, runID)
	if err != nil {
		return nil, err
	}
	history, err := trainingdb.ListTrainingJobEvents(ctx, db, runID, from)
	if err != nil {
		log.WithError(err).Error("training: read training job events failed")
		return nil, internalErr("failed to read the training job events", err)
	}
	out := make([]FeedEvent, 0, len(history))
	for _, e := range history {
		var ev sidecar.TrainingEvent
		if json.Unmarshal([]byte(e.Data), &ev) != nil {
			continue
		}
		ev.Seq = e.Seq
		out = append(out, FeedEvent{RunID: runID, JobID: run.JobID, TrainingEvent: ev})
	}
	return out, nil
}
