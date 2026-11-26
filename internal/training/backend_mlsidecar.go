package training

// The Backend of the LoRA fine-tuning: the jobs run on the ML sidecar (Modal today), see backend.go.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/sys/ml_sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

// JobStarter starts training jobs on the ML sidecar; *ml_sidecar.Client is one.
type JobStarter interface {
	StartTrainingJob(ctx context.Context,
		req ml_sidecar.StartTrainingJobRequest) (ml_sidecar.TrainingJob, error)
}

// JobCanceller asks the ML sidecar to stop training jobs; *ml_sidecar.Client is one.
type JobCanceller interface {
	CancelTrainingJob(ctx context.Context,
		jobID string) error
}

// JobStreamer streams the events of a training job of the ML sidecar; *ml_sidecar.Client is one.
type JobStreamer interface {
	StreamTrainingJobEvents(ctx context.Context,
		jobID string,
		from int,
		fn func(ml_sidecar.TrainingEvent) error) error
}

// MLSidecarJobs is what the LoRA backend needs from the ML sidecar.
type MLSidecarJobs interface {
	JobStarter
	JobCanceller
	JobStreamer
}

// MLSidecarBackend is the Backend of the LoRA jobs.
type MLSidecarBackend struct {
	db       *sql.DB
	datasets DatasetStore
	sc       MLSidecarJobs
}

// NewMLSidecarBackend is the LoRA backend over the ML sidecar sc; the datasets are those of the store, db is the database they are listed in.
func NewMLSidecarBackend(db *sql.DB,
	datasets DatasetStore,
	sc MLSidecarJobs) *MLSidecarBackend {
	return &MLSidecarBackend{
		db:       db,
		datasets: datasets,
		sc:       sc,
	}
}

// LoRAParams is the body of the request that starts a LoRA job: only these fields come from the client; the dataset's content is added
// from the dataset's file.
type LoRAParams struct {
	NumTrainSteps int    `json:"num_train_steps"`
	ModelSize     string `json:"model_size"`
	Dataset       string `json:"dataset"`
}

// ModelType is trainingdb.ModelTypeLoRA.
func (b *MLSidecarBackend) ModelType() string { return trainingdb.ModelTypeLoRA }

// Owns is true: the monitor of every instance follows the ML sidecar's jobs (the stream is of the sidecar, not of a process).
func (b *MLSidecarBackend) Owns(trainingdb.TrainingJobRun) bool { return true }

// Start starts a training job on the ML sidecar through sc (the ML sidecar's REST API) and records the TrainingJob the ML sidecar reports.
// Errors are *Error: KindInvalid for a bad request (also one the ML sidecar rejects as invalid, with its message), KindUpstream for an ML
// sidecar error, KindInternal.
func (b *MLSidecarBackend) Start(ctx context.Context,
	in StartInput) (*trainingdb.TrainingJobRun, error) {
	var p LoRAParams
	if len(in.Params) != 0 {
		if err := json.Unmarshal(in.Params, &p); err != nil {
			return nil, invalidErr("invalid JSON body")
		}
	}
	req := ml_sidecar.StartTrainingJobRequest{
		NumTrainSteps: p.NumTrainSteps,
		ModelSize:     strings.ToLower(strings.TrimSpace(p.ModelSize)),
	}
	name := strings.TrimSpace(p.Dataset)
	if name == "" {
		return nil, invalidErr("dataset is required: the name of a training dataset")
	}
	if req.NumTrainSteps < 0 {
		return nil, invalidErr("num_train_steps must be positive")
	}
	switch req.ModelSize {
	case "", ml_sidecar.ModelSize4B, ml_sidecar.ModelSize12B, ml_sidecar.ModelSize31B:
	default:
		return nil, invalidErr("model_size must be \"4b\", \"12b\" or \"31b\"")
	}

	dataset, content, err := LoadTrainingDataset(ctx,
		b.db,
		b.datasets,
		name)
	if errors.Is(err, ErrUnknownDataset) {
		return nil, invalidErr("unknown dataset " + name + ": there is no dataset of that name in the training datasets")
	} else if err != nil {
		return nil, internalErr(err.Error(), err)
	}
	req.Dataset, req.DatasetContent = dataset.Filename, base64.StdEncoding.EncodeToString(content)
	job, err := b.sc.StartTrainingJob(ctx, req)
	if err != nil {
		var se *ml_sidecar.StatusError
		if errors.As(err, &se) && se.Code == http.StatusBadRequest {
			return nil, &Error{KindInvalid, se.Message(), err}
		}
		log.WithError(err).Error("training: start training job failed")
		return nil, &Error{KindUpstream, "ML sidecar failed to start the training job", err}
	}
	// The job is running now: record it even if the caller went away meanwhile.
	ctx = context.WithoutCancel(ctx)
	// the ML sidecar has taken this version of the file
	if _, err := trainingdb.MarkTrainingDatasetSynced(ctx,
		b.db,
		dataset.ID,
		dataset.SHA256); err != nil {
		log.WithError(err).WithField("filename", dataset.Filename).Error("training: mark training dataset synced failed")
	}
	run, err := in.Record(ctx,
		trainingdb.TrainingJobRun{
			UserID:        in.UserID,
			JobID:         job.JobID,
			ProviderJobID: job.ProviderJobID,
			Status:        job.Status,
			ModelSize:     job.ModelSize,
			Dataset:       job.Dataset,
			NumTrainSteps: job.NumTrainSteps,
			StartedAt:     job.StartedAt,
		})
	if err != nil {
		log.WithError(err).WithField("job_id", job.JobID).Error("training: record training job run failed")
		return nil, internalErr("training job "+job.JobID+" started but could not be recorded", err)
	}
	// the meta of the run: a failure to store it is logged, the job runs anyway
	if err := trainingdb.UpsertTrainingJobRunMeta(ctx,
		b.db,
		loraMeta(*run,
			*dataset)); err != nil {
		log.WithError(err).WithField("run_id", run.ID).Warn("training: recording the run meta failed")
	}
	return run, nil
}

// Stream follows the ML sidecar's event stream of the job.
func (b *MLSidecarBackend) Stream(ctx context.Context,
	run trainingdb.TrainingJobRun,
	from int,
	fn func(monitoring.TrainingEvent) error) error {
	return b.sc.StreamTrainingJobEvents(ctx,
		run.JobID,
		from,
		fn)
}

// Cancel asks the ML sidecar to stop the job: it ends at its next step (or at the next stage of its setup).
func (b *MLSidecarBackend) Cancel(ctx context.Context,
	run trainingdb.TrainingJobRun) error {
	if err := b.sc.CancelTrainingJob(ctx, run.JobID); err != nil {
		var se *ml_sidecar.StatusError
		if errors.As(err, &se) && se.Code == http.StatusConflict {
			return &Error{KindConflict, se.Message(), err}
		}
		log.WithError(err).WithField("job_id", run.JobID).Error("training: stop training job failed")
		return &Error{KindUpstream, "ML sidecar failed to stop the training job", err}
	}
	return nil
}

// Model is the model of the "model" of the completed event: the weights the job saved. A completed event without them (from an ML sidecar that
// does not report them yet) makes none.
func (b *MLSidecarBackend) Model(_ context.Context,
	run trainingdb.TrainingJobRun,
	ev monitoring.TrainingEvent) (*ModelRecord, error) {
	info := ev.Model
	if info == nil || info.Path == "" {
		log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID}).Warn("training: the job completed without reporting its saved model: no trained model recorded (redeploy the Modal app)")
		return nil, nil
	}
	tm := trainingdb.TrainedModel{
		UserID:        run.UserID,
		RunID:         &run.ID,
		JobID:         run.JobID,
		BaseModel:     info.BaseModel,
		ModelSize:     info.ModelSize,
		Dataset:       run.Dataset,
		DatasetSHA256: info.DatasetSHA256,
		NumExamples:   info.NumExamples,
		NumTrainSteps: run.NumTrainSteps,
		FinalLoss:     ev.FinalLoss,
		Params:        info.Params,
		Path:          info.Path,
		SizeBytes:     info.SizeBytes,
	}
	if tm.ModelSize == "" {
		tm.ModelSize = run.ModelSize
	}
	for _, f := range info.Files {
		tm.Files = append(tm.Files, trainingdb.TrainedModelFile{
			Path:      f.Path,
			SizeBytes: f.SizeBytes,
			SHA256:    f.SHA256,
		})
	}
	return &ModelRecord{
		Model: tm,
		Info:  info,
	}, nil
}

// Ended does nothing: the ML sidecar keeps no state of ours.
func (b *MLSidecarBackend) Ended(context.Context,
	trainingdb.TrainingJobRun,
	monitoring.TrainingEvent,
	int64) {
}

// Reap does nothing: the ML sidecar's jobs are followed by every instance, a job of a stopped instance is picked up by the next monitor.
func (b *MLSidecarBackend) Reap(context.Context,
	string) error {
	return nil
}

// LoRAMetaSchema names the shape of the Meta of a LoRA run (gb_training_job_runs_meta.meta_schema).
const LoRAMetaSchema = "lora.v1"

// LoRAMeta is what only a LoRA run has of the training configuration: the JSON meta of its gb_training_job_runs_meta row. The LoRA settings
// the ML sidecar chooses itself (rank, alpha, learning rate, ...) are not known here when the job starts.
type LoRAMeta struct {
	ModelSize   string `json:"model_size"`
	DatasetFile string `json:"dataset_file"`
}

// loraMeta is the meta row of a LoRA run that starts on dataset.
func loraMeta(run trainingdb.TrainingJobRun,
	dataset trainingdb.TrainingDataset) trainingdb.TrainingJobRunMeta {
	meta, _ := json.Marshal(LoRAMeta{
		ModelSize:   run.ModelSize,
		DatasetFile: dataset.Filename,
	})
	records := dataset.NumRecords
	return trainingdb.TrainingJobRunMeta{
		RunID:            run.ID,
		UserID:           run.UserID,
		ModelType:        trainingdb.ModelTypeLoRA,
		DatasetSHA256:    dataset.SHA256,
		NumTrainExamples: &records,
		MetaSchema:       LoRAMetaSchema,
		Meta:             meta,
	}
}
