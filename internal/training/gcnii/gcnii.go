// Package gcnii is the training Backend (internal/training/backend.go) of the GCNII* graph classifier, which runs on sargo (rust/sargo): the
// one pipeline of internal/training (Monitor, StartJob, StopJob, the run rows and the SSE feed) runs these jobs like the LoRA ones.
//
// The model is the user's: every user has their own, and trains one at a time. Sargo has no event stream, so Stream follows the job by polling
// its status and turns what changed into the events of every training job (status, progress with the epoch and its loss, the end).
//
// Cluster. Several gligabao instances take requests behind one load balancer; each runs its own sargo with a local directory, and they share
// only the database and the bucket. So a training lives in the sargo of ONE instance (the one in Info.Instance of the run), and the others
// learn about it from the database:
//
//   - the run row (gb_training_job_runs, model_type "gcnii") is what the API shows, on every instance.
//   - gb_model_train_jobs has a lease row per training: it is made (claimed) before sargo is asked, and the unique index of running rows per
//     user refuses a second training of the user on any instance. The owner instance beats it while it follows its sargo; a running lease
//     without a beat is marked lost by whoever looks next (Reap), and its run is marked failed.
//   - when sargo is done the owner copies the model into the store, records it in gb_trained_models (model_type "gcnii", with the user) and
//     ends the lease.
//   - before a model is used (infer, embed) the instance compares the model in its sargo directory (a marker file with the model row ID) with
//     the latest row of the user and, if they differ, writes the latest one into its directory (Sync). Sargo reloads the file when it changes.
package gcnii

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/modeltrain"
	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	"github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

// Service is what the backend needs from sargo; *sargo.Client is one.
type Service interface {
	GCNIITrain(ctx context.Context,
		req *sargo.GCNIITrainRequest) (*sargo.GCNIITrainResponse, error)
	GCNIITrainStatus(ctx context.Context,
		userID string) (*sargo.GCNIIStatus, error)
	GCNIICancel(ctx context.Context,
		userID string) (*sargo.GCNIIStatus, error)
}

// Params is the body of the request that starts a GCNII* job (POST /v1/gb/training/jobs with model_type "gcnii"). The graphs are those of a
// dataset of the user with the classes an LLM gave them when they were remembered (Dataset, cut up by the fields of modeltrain.Options), or,
// without a dataset, the labelled Graphs of the body.
type Params struct {
	// Dataset is the name of a live dataset of the user (empty: the default one); ignored if Graphs is given.
	Dataset string `json:"dataset"`
	// Graphs are labelled graphs (each with a class) to train on, instead of a dataset.
	Graphs     []sargo.GCNIIGraph `json:"graphs"`
	Validation []sargo.GCNIIGraph `json:"validation"`
	// Classes, ClassPick, TopClasses, OtherClass, MinEdges, MinGraphsPerClass, ValidationFraction and SplitSeed are the fields of
	// modeltrain.Options (they apply to a dataset).
	Classes            []string `json:"classes"`
	ClassPick          string   `json:"class_pick"`
	TopClasses         int      `json:"top_classes"`
	OtherClass         bool     `json:"other_class"`
	MinEdges           int      `json:"min_edges"`
	MinGraphsPerClass  int      `json:"min_graphs_per_class"`
	ValidationFraction float64  `json:"validation_fraction"`
	SplitSeed          uint64   `json:"split_seed"`
	Epochs             int      `json:"epochs"`
	LR                 float64  `json:"lr"`
	Batch              int      `json:"batch"`
	Hidden             int      `json:"hidden"`
	Depth              int      `json:"depth"`
	Seed               *uint64  `json:"seed"`
	// Task is "classify" (the default) or "link"; Holdout, Encoder, ClassWeight and Init are those of sargo.GCNIITrainRequest (link only).
	Task        string  `json:"task"`
	Holdout     float64 `json:"holdout"`
	Encoder     string  `json:"encoder"`
	ClassWeight float64 `json:"class_weight"`
	Init        *bool   `json:"init"`
}

// Info is what the request knew and sargo does not report: the Info of the run row.
type Info struct {
	// Instance is the gligabao process whose sargo trains the job (and so the only one that follows it and has the weights).
	Instance string `json:"instance"`
	Dataset  string `json:"dataset,omitempty"`
	// Run is the ID of the record of the training in the history ("" if none).
	Run              string                 `json:"run,omitempty"`
	Params           modeltrain.TrainParams `json:"params"`
	Graphs           int                    `json:"graphs"`
	ValidationGraphs int                    `json:"validation_graphs"`
	Classes          []string               `json:"classes,omitempty"`
}

// Config is what a Backend is made of.
type Config struct {
	// Svc is sargo.
	Svc Service
	// DB is the database of the training tables and the relational data (the graphs of the datasets).
	DB *sql.DB
	// Weights is where the trained models are stored; nil: nowhere (they stay in the directory of sargo, are not recorded and there
	// is no lease: a second training of a user is refused by that sargo only).
	Weights *modeltrain.Weights
	// Hook picks the classes of the graphs of a dataset (nil: the classes an LLM gave them, see modeltrain.DefaultHook).
	Hook memory_core.ClassifyingTrainingHook
	// HistoryDir is where the history of the trainings on datasets is kept ("" means none).
	HistoryDir string
	// PollEvery is how often the status of a job is read (default 2 seconds); BeatStale how long without a beat makes a running lease lost
	// (default 90 seconds).
	PollEvery time.Duration
	BeatStale time.Duration
}

// Backend is the training backend of sargo's GCNII* models.
type Backend struct {
	cfg      Config
	instance string // identifies this process in Info.Instance and gb_model_train_jobs.instance_id
}

// New is the backend of cfg.
func New(cfg Config) *Backend {
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = 2 * time.Second
	}
	if cfg.BeatStale <= 0 {
		cfg.BeatStale = 90 * time.Second
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return &Backend{
		cfg:      cfg,
		instance: hex.EncodeToString(id),
	}
}

// ModelType is trainingdb.ModelTypeGCNII.
func (b *Backend) ModelType() string { return trainingdb.ModelTypeGCNII }

func infoOf(run trainingdb.TrainingJobRun) Info {
	var info Info
	_ = json.Unmarshal(run.Info, &info)
	return info
}

// Owns is true for the runs whose sargo is this instance's.
func (b *Backend) Owns(run trainingdb.TrainingJobRun) bool {
	return infoOf(run).Instance == b.instance
}

// defaultEpochs is the epochs sargo trains for 0.
const defaultEpochs = 30

// Start builds the training and validation sets, claims the training of the user (training.KindConflict if they have one running anywhere
// in the cluster), records the run and asks sargo to train. The run is recorded before sargo is asked, so a refusal ends it as failed.
func (b *Backend) Start(ctx context.Context,
	in training.StartInput) (*trainingdb.TrainingJobRun, error) {
	var p Params
	if len(in.Params) != 0 {
		if err := json.Unmarshal(in.Params, &p); err != nil {
			return nil, &training.Error{Kind: training.KindInvalid, Msg: "invalid JSON body", Err: err}
		}
	}
	req, info, opts, sets, startErr := b.build(ctx,
		in.UserID,
		p)
	if startErr != nil {
		return nil, startErr
	}
	info.Instance = b.instance
	history := b.newHistory(in.UserID,
		info,
		opts,
		sets)
	if history != nil {
		info.Run = history.ID
	}

	jobID, err := b.claim(ctx,
		in.UserID,
		info)
	if errors.Is(err, trainingdb.ErrModelTrainJobRunning) {
		return nil, &training.Error{Kind: training.KindConflict, Msg: "a training for this user is already running", Err: err}
	} else if err != nil {
		log.WithError(err).WithField("user_id", in.UserID).Error("gcnii: claiming the training failed")
		return nil, &training.Error{Kind: training.KindInternal, Msg: "failed to start the training", Err: err}
	}
	infoJSON, _ := json.Marshal(info)
	epochs := p.Epochs
	if epochs <= 0 {
		epochs = defaultEpochs
	}
	run, err := in.Record(ctx,
		trainingdb.TrainingJobRun{
			UserID:        in.UserID,
			JobID:         jobID,
			Status:        "running",
			Dataset:       info.Dataset,
			NumTrainSteps: epochs,
			StartedAt:     time.Now().UTC(),
			Info:          infoJSON,
		})
	if err != nil {
		b.endClaim(ctx, jobID, err)
		log.WithError(err).WithField("job_id", jobID).Error("gcnii: record training job run failed")
		return nil, &training.Error{Kind: training.KindInternal, Msg: "failed to record the training job run", Err: err}
	}
	var metaOpts *modeltrain.Options
	if len(p.Graphs) == 0 {
		metaOpts = &opts
	}
	b.recordMeta(ctx,
		newMeta(*run,
			p,
			metaOpts,
			info.Run,
			epochs,
			req.Graphs,
			req.Validation))
	if _, err := b.cfg.Svc.GCNIITrain(ctx, req); err != nil {
		b.endClaim(ctx, jobID, err)
		in.Abort(ctx, *run, err)
		return nil, failSargo("start the training", in.UserID, err)
	}
	// the history record is written once the training runs: a history that cannot be written is logged, not an error of the request
	b.saveHistory(history)
	return run, nil
}

// build turns the params into the request to sargo and the Info of the run, or an *training.Error.
func (b *Backend) build(ctx context.Context,
	userID string,
	p Params) (*sargo.GCNIITrainRequest, Info, modeltrain.Options, modeltrain.Sets, *training.Error) {
	params := modeltrain.TrainParams{
		Epochs: p.Epochs,
		LR:     p.LR,
		Batch:  p.Batch,
		Hidden: p.Hidden,
		Depth:  p.Depth,
		Seed:   p.Seed,

		Task:        p.Task,
		Holdout:     p.Holdout,
		Encoder:     p.Encoder,
		ClassWeight: p.ClassWeight,
		Init:        p.Init,
	}
	req := &sargo.GCNIITrainRequest{
		UserID: userID,
		Epochs: p.Epochs,
		LR:     p.LR,
		Batch:  p.Batch,
		Hidden: p.Hidden,
		Depth:  p.Depth,
		Seed:   p.Seed,

		Task:        p.Task,
		Holdout:     p.Holdout,
		Encoder:     p.Encoder,
		ClassWeight: p.ClassWeight,
		Init:        p.Init,
	}
	info := Info{Params: params}
	if len(p.Graphs) != 0 {
		req.Graphs, req.Validation = p.Graphs, p.Validation
		info.Graphs, info.ValidationGraphs = len(p.Graphs), len(p.Validation)
		if err := req.Validate(); err != nil {
			return nil, info, modeltrain.Options{}, modeltrain.Sets{}, &training.Error{Kind: training.KindInvalid, Msg: err.Error(), Err: err}
		}
		return req, info, modeltrain.Options{}, modeltrain.Sets{}, nil
	}
	dataset, err := memory_core.ResolveDatasetName(p.Dataset)
	if err != nil {
		return nil, info, modeltrain.Options{}, modeltrain.Sets{}, buildErr(userID, p.Dataset, err)
	}
	opts := modeltrain.Options{
		Classes:            p.Classes,
		ClassPick:          p.ClassPick,
		TopClasses:         p.TopClasses,
		OtherClass:         p.OtherClass,
		MinEdges:           p.MinEdges,
		MinGraphsPerClass:  p.MinGraphsPerClass,
		ValidationFraction: p.ValidationFraction,
		Seed:               p.SplitSeed,
	}
	sets, err := modeltrain.Build(ctx,
		b.cfg.DB,
		b.cfg.Hook,
		userID,
		dataset,
		opts)
	if err != nil {
		return nil, info, opts, modeltrain.Sets{}, buildErr(userID, dataset, err)
	}
	req.Graphs, req.Validation = sets.Train, sets.Validation
	info.Dataset, info.Classes = dataset, sets.Classes
	info.Graphs, info.ValidationGraphs = len(sets.Train), len(sets.Validation)
	return req, info, opts, sets, nil
}

// buildErr is the *training.Error of an error of modeltrain.Build.
func buildErr(userID,
	dataset string,
	err error) *training.Error {
	switch {
	case errors.Is(err, modeltrain.ErrInvalidOptions), errors.Is(err, memory_core.ErrInvalidDataset):
		return &training.Error{Kind: training.KindInvalid, Msg: err.Error(), Err: err}
	case errors.Is(err, memory_core.ErrDatasetNotFound):
		return &training.Error{Kind: training.KindNotFound, Msg: "dataset not found", Err: err}
	case errors.Is(err, modeltrain.ErrNoGraphs), errors.Is(err, modeltrain.ErrTooFewClasses):
		return &training.Error{Kind: training.KindInvalid, Msg: err.Error() + " (remember the dataset with classify_graph on)", Err: err}
	case errors.Is(err, memory_core.ErrInvalidTrainingDataset):
		return &training.Error{Kind: training.KindUpstream, Msg: err.Error(), Err: err}
	}
	log.WithError(err).WithFields(log.Fields{"user_id": userID, "dataset": dataset}).Error("gcnii: building the training set failed")
	return &training.Error{Kind: training.KindInternal, Msg: "failed to build the training set", Err: err}
}

// failSargo is the *training.Error of an error of a call to sargo: what sargo says is wrong with the request (400, 404, 409) and that it is
// not set up (503) is passed on as what it means; anything else is an upstream failure (sargo down or answered garbage).
func failSargo(what,
	userID string,
	err error) *training.Error {
	var apiErr *sargo.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 400:
			return &training.Error{Kind: training.KindInvalid, Msg: apiErr.Message, Err: err}
		case 404:
			return &training.Error{Kind: training.KindNotFound, Msg: apiErr.Message, Err: err}
		case 409:
			return &training.Error{Kind: training.KindConflict, Msg: apiErr.Message, Err: err}
		case 503:
			return &training.Error{Kind: training.KindUnavailable, Msg: apiErr.Message, Err: err}
		}
	}
	if errors.Is(err, sargo.ErrInvalidGCNII) {
		return &training.Error{Kind: training.KindInvalid, Msg: err.Error(), Err: err}
	}
	log.WithError(err).WithField("user_id", userID).Error("gcnii: " + what + " failed")
	return &training.Error{Kind: training.KindUpstream, Msg: "sargo failed to " + what, Err: err}
}

// recordMeta stores the meta of a run; one that cannot be stored is logged (the run does not depend on it).
func (b *Backend) recordMeta(ctx context.Context,
	m trainingdb.TrainingJobRunMeta) {
	if err := trainingdb.UpsertTrainingJobRunMeta(context.WithoutCancel(ctx),
		b.cfg.DB,
		m); err != nil {
		log.WithError(err).WithField("run_id", m.RunID).Warn("gcnii: recording the run meta failed")
	}
}

// newHistory is the record of the training in the history (nil if there is no history directory, or the training is not on a dataset).
func (b *Backend) newHistory(userID string,
	info Info,
	opts modeltrain.Options,
	sets modeltrain.Sets) *modeltrain.Run {
	if b.cfg.HistoryDir == "" || info.Dataset == "" {
		return nil
	}
	run := modeltrain.NewRun(userID,
		info.Dataset,
		time.Now(),
		opts,
		info.Params,
		sets)
	return &run
}

// saveHistory writes the record to the history; one that cannot be written is logged.
func (b *Backend) saveHistory(run *modeltrain.Run) {
	if run == nil {
		return
	}
	if err := modeltrain.SaveRun(b.cfg.HistoryDir, *run); err != nil {
		log.WithError(err).WithFields(log.Fields{"user_id": run.UserID, "dataset": run.Dataset}).Warn("gcnii: saving the run history failed")
	}
}

// Stream follows the job of the run by polling sargo, and turns what changed into events. The first call of a run has the "running" status
// event; a progress event follows every finished epoch (sargo reports the loss of each, so a slow poll skips none); the event that ends the job is a "completed"
// (its Data is sargo's job: accuracy, loss, val_accuracy, val_loss, classes, ...), "failed" or "cancelled" status. It returns nil after it.
func (b *Backend) Stream(ctx context.Context,
	run trainingdb.TrainingJobRun,
	from int,
	fn func(monitoring.TrainingEvent) error) error {
	seq := from
	emit := func(ev monitoring.TrainingEvent) error {
		ev.Seq = seq
		ev.Timestamp = float64(time.Now().UnixNano()) / 1e9
		seq++
		return fn(ev)
	}
	if from == 0 {
		if err := emit(monitoring.TrainingEvent{Type: monitoring.EventStatus, Status: "running", MaxSteps: &run.NumTrainSteps}); err != nil {
			return err
		}
	}
	emitted := 0 // epochs already sent as progress events
	// emitEpochs sends one progress event for each epoch of the job not sent yet (sargo keeps every epoch's loss, so none is skipped).
	emitEpochs := func(job *sargo.GCNIIJob) error {
		send := func(epoch int, loss float64) error {
			step, l := epoch, loss
			return emit(monitoring.TrainingEvent{
				Type:     monitoring.EventProgress,
				Step:     &step,
				Loss:     &l,
				MaxSteps: &run.NumTrainSteps,
				Message:  fmt.Sprintf("epoch %d/%d", epoch, job.Epochs),
			})
		}
		if len(job.EpochLosses) == 0 && job.EpochLoss != nil && job.Epoch > emitted { // sargo without epoch_losses: the last epoch only
			emitted = job.Epoch
			return send(job.Epoch, *job.EpochLoss)
		}
		for emitted < len(job.EpochLosses) {
			emitted++
			if err := send(emitted, job.EpochLosses[emitted-1]); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		st, err := b.cfg.Svc.GCNIITrainStatus(ctx, run.UserID)
		if err != nil {
			return err // no beat: sargo may be gone; the monitor retries
		}
		job := st.Job
		b.beat(ctx, run, job)
		switch {
		case job == nil || job.StartedUnix < run.StartedAt.Unix()-5:
			return emit(monitoring.TrainingEvent{
				Type:   monitoring.EventStatus,
				Status: trainingdb.TrainingJobFailed,
				Error:  "sargo lost the training (it restarted, or another training replaced it)",
			})
		case job.Status == sargo.GCNIIJobRunning:
			if err := emitEpochs(job); err != nil {
				return err
			}
		case job.Status == sargo.GCNIIJobDone:
			if err := emitEpochs(job); err != nil { // the epochs that finished since the last poll
				return err
			}
			return emit(monitoring.TrainingEvent{
				Type:      monitoring.EventStatus,
				Status:    trainingdb.TrainingJobCompleted,
				FinalLoss: job.Loss,
				Message:   scoresText(*job),
				Data:      mapOf(*job),
			})
		case job.Status == sargo.GCNIIJobCancelled:
			return emit(monitoring.TrainingEvent{
				Type:   monitoring.EventStatus,
				Status: trainingdb.TrainingJobCancelled,
				Error:  "stopped",
			})
		default:
			return emit(monitoring.TrainingEvent{
				Type:   monitoring.EventStatus,
				Status: trainingdb.TrainingJobFailed,
				Error:  job.Error,
			})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(b.cfg.PollEvery):
		}
	}
}

// scoresText is the scores of a finished job in words.
func scoresText(job sargo.GCNIIJob) string {
	text := fmt.Sprintf("%d graphs, %d epochs", job.Graphs, job.Epochs)
	for _, s := range []struct {
		name string
		v    *float64
	}{{"accuracy", job.Accuracy}, {"loss", job.Loss}, {"val accuracy", job.ValAccuracy}, {"val loss", job.ValLoss}} {
		if s.v != nil {
			text += fmt.Sprintf(", %s %.4f", s.name, *s.v)
		}
	}
	return text
}

// mapOf is the job as the generic map of the Data of an event.
func mapOf(job sargo.GCNIIJob) map[string]any {
	raw, _ := json.Marshal(job)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// Cancel asks sargo to stop the training of the user of the run, if this instance's sargo trains it (training.KindConflict if another
// instance does, or sargo has no running training of the user).
func (b *Backend) Cancel(ctx context.Context,
	run trainingdb.TrainingJobRun) error {
	if !b.Owns(run) {
		return &training.Error{Kind: training.KindConflict, Msg: "the training runs on another instance of the server"}
	}
	if _, err := b.cfg.Svc.GCNIICancel(ctx, run.UserID); err != nil {
		return failSargo("stop the training", run.UserID, err)
	}
	return nil
}
