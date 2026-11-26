package gcnii

// The cluster part of the backend: the lease of a training (gb_model_train_jobs), the model the job made and the weights of the models (see the
// package comment).

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/modeltrain"
	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	"github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

// claim records, before sargo is asked to train, that this instance trains the model of the user, and returns the ID of the job (the one of the
// model it makes): trainingdb.ErrModelTrainJobRunning if the user has a running training anywhere in the cluster. Without a store of the weights
// nothing is claimed.
func (b *Backend) claim(ctx context.Context,
	userID string,
	info Info) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	jobID := modeltrain.NewJobID(time.Now().Unix(), suffix)
	if b.cfg.Weights == nil {
		return jobID, nil
	}
	infoJSON, err := json.Marshal(info)
	if err != nil {
		return "", err
	}
	_, err = trainingdb.ClaimModelTrainJob(ctx,
		b.cfg.DB,
		trainingdb.ModelTrainJob{
			JobID:      jobID,
			ModelType:  trainingdb.ModelTypeGCNII,
			UserID:     userID,
			InstanceID: b.instance,
			Info:       infoJSON,
		},
		time.Now().Add(-b.cfg.BeatStale))
	return jobID, err
}

// endClaim ends the lease of a training that was not started (it failed).
func (b *Backend) endClaim(ctx context.Context,
	jobID string,
	cause error) {
	b.endLease(ctx,
		jobID,
		trainingdb.ModelTrainJobFailed,
		cause.Error(),
		"{}",
		nil)
}

func (b *Backend) endLease(ctx context.Context,
	jobID,
	status,
	errText,
	jobJSON string,
	modelID *int64) {
	if b.cfg.Weights == nil {
		return
	}
	if err := trainingdb.EndModelTrainJob(context.WithoutCancel(ctx),
		b.cfg.DB,
		jobID,
		status,
		errText,
		jobJSON,
		modelID); err != nil {
		log.WithError(err).WithField("job_id", jobID).Error("gcnii: ending the lease of the training failed")
	}
}

// beat tells the others this instance is alive and following the job, with the job's state.
func (b *Backend) beat(ctx context.Context,
	run trainingdb.TrainingJobRun,
	job *sargo.GCNIIJob) {
	if b.cfg.Weights == nil || job == nil {
		return
	}
	raw, _ := json.Marshal(job)
	if _, err := trainingdb.BeatModelTrainJob(ctx,
		b.cfg.DB,
		run.JobID,
		b.instance,
		string(raw)); err != nil {
		log.WithError(err).WithField("job_id", run.JobID).Warn("gcnii: recording the beat failed")
	}
}

// Model stores the weights of the completed job and makes the row of the model (nil without a store of the weights). The marker of the
// directory of sargo is written last (ModelRecord.Done): until then the job is still running and Sync leaves the directory alone.
func (b *Backend) Model(ctx context.Context,
	run trainingdb.TrainingJobRun,
	ev monitoring.TrainingEvent) (*training.ModelRecord, error) {
	if b.cfg.Weights == nil {
		log.WithField("job_id", run.JobID).Warn("gcnii: no store of the weights: the trained model is not recorded")
		return nil, nil
	}
	var job sargo.GCNIIJob
	raw, err := json.Marshal(ev.Data)
	if err != nil || json.Unmarshal(raw, &job) != nil {
		return nil, fmt.Errorf("the completed event has no job")
	}
	info := infoOf(run)
	files, err := b.cfg.Weights.Save(ctx,
		run.UserID,
		run.JobID)
	if err != nil {
		return nil, fmt.Errorf("storing the weights: %w", err)
	}
	params, err := json.Marshal(modeltrain.WeightsParams{
		Run:         info.Run,
		Train:       info.Params,
		Classes:     job.Classes,
		Accuracy:    job.Accuracy,
		Loss:        job.Loss,
		ValAccuracy: job.ValAccuracy,
		ValLoss:     job.ValLoss,
		LinkEval:    job.LinkEval,
		ValLinkEval: job.ValLinkEval,
	})
	if err != nil {
		return nil, err
	}
	m, err := b.cfg.Weights.NewModel(modeltrain.ModelInfo{
		UserID:           run.UserID,
		JobID:            run.JobID,
		Dataset:          info.Dataset,
		Graphs:           job.Graphs,
		ValidationGraphs: job.ValidationGraphs,
		Epochs:           job.Epochs,
		Loss:             job.Loss,
		Params:           params,
	},
		files)
	if err != nil {
		return nil, err
	}
	m.RunID = &run.ID
	return &training.ModelRecord{
		Model: m,
		Done: func(_ context.Context,
			modelID int64) error {
			return b.cfg.Weights.WriteMarker(run.UserID, modelID)
		},
	}, nil
}

// Ended ends the lease of the training, with the model it made.
func (b *Backend) Ended(ctx context.Context,
	run trainingdb.TrainingJobRun,
	ev monitoring.TrainingEvent,
	modelID int64) {
	status, errText := trainingdb.ModelTrainJobDone, ""
	if ev.Status != trainingdb.TrainingJobCompleted {
		status, errText = trainingdb.ModelTrainJobFailed, ev.Error
	}
	raw, _ := json.Marshal(ev.Data)
	if len(ev.Data) == 0 {
		raw = []byte("{}")
	}
	if ev.Status == trainingdb.TrainingJobCompleted {
		// the results of the job go to the meta of the run (a run made before the meta existed has none)
		if m, err := trainingdb.GetTrainingJobRunMeta(ctx,
			b.cfg.DB,
			run.ID); err == nil {
			b.recordMeta(ctx,
				withResults(*m,
					ev.Data))
		}
	}
	var mid *int64
	if modelID != 0 {
		mid = &modelID
	}
	b.endLease(ctx,
		run.JobID,
		status,
		errText,
		string(raw),
		mid)
}

// Reap marks the unfinished runs of the user whose training nobody follows any more as failed: the lease of the run was marked lost (its instance
// stopped beating), or, without a store of the weights (no leases), this instance did not start it.
func (b *Backend) Reap(ctx context.Context,
	userID string) error {
	var lease *trainingdb.ModelTrainJob
	if b.cfg.Weights != nil {
		var err error
		if lease, err = b.latestLease(ctx, userID); err != nil {
			return err
		}
	}
	runs, err := trainingdb.ListUserTrainingJobRuns(ctx,
		b.cfg.DB,
		userID,
		20)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Type() != trainingdb.ModelTypeGCNII || trainingdb.TrainingJobFinished(run.Status) {
			continue
		}
		lost := false
		if b.cfg.Weights == nil {
			lost = !b.Owns(run)
		} else {
			lost = lease != nil && lease.JobID == run.JobID && lease.Status == trainingdb.ModelTrainJobLost
		}
		if !lost {
			continue
		}
		now := time.Now().UTC()
		if err := trainingdb.UpdateTrainingJobRun(ctx,
			b.cfg.DB,
			run.ID,
			trainingdb.TrainingJobRunUpdate{
				Status:     trainingdb.TrainingJobFailed,
				Error:      "the instance that ran the training stopped before it ended",
				FinishedAt: &now,
			}); err != nil {
			return err
		}
	}
	return nil
}

// latestLease is the newest lease of the user, after marking the running ones that stopped beating as lost; nil if there is none.
func (b *Backend) latestLease(ctx context.Context,
	userID string) (*trainingdb.ModelTrainJob, error) {
	if _, err := trainingdb.ExpireModelTrainJobs(ctx,
		b.cfg.DB,
		trainingdb.ModelTypeGCNII,
		userID,
		time.Now().Add(-b.cfg.BeatStale)); err != nil {
		return nil, err
	}
	job, err := trainingdb.LatestModelTrainJob(ctx,
		b.cfg.DB,
		trainingdb.ModelTypeGCNII,
		userID)
	if errors.Is(err, trainingdb.ErrNotFound) {
		return nil, nil
	}
	return job, err
}

// Sync makes the model in sargo's directory of the user the latest recorded one: if the marker of the directory is not the ID of the latest
// model (a model that another instance trained, or a directory that was lost with its machine) the files of that row are written back. It does
// nothing while this instance trains for the user (the directory then holds the newest model, not yet recorded), when the user has no recorded
// model, and without a store of the weights. It says whether it restored.
func (b *Backend) Sync(ctx context.Context,
	userID string) (bool, error) {
	if b == nil || b.cfg.Weights == nil {
		return false, nil
	}
	lease, err := b.latestLease(ctx, userID)
	if err != nil {
		return false, err
	}
	if lease != nil && lease.Status == trainingdb.ModelTrainJobRunning && lease.InstanceID == b.instance {
		return false, nil
	}
	models, err := trainingdb.ListUserTrainedModels(ctx,
		b.cfg.DB,
		trainingdb.ModelTypeGCNII,
		userID,
		1)
	if err != nil || len(models) == 0 || b.cfg.Weights.Marker(userID) == models[0].ID {
		return false, err
	}
	if err := b.cfg.Weights.Restore(ctx, models[0]); err != nil {
		return false, err
	}
	log.WithFields(log.Fields{"user_id": userID, "model_id": models[0].ID}).Info("gcnii: the latest recorded model was written to the directory of sargo")
	return true, nil
}
