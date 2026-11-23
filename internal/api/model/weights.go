package model

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/modeltrain"
	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

// The weights of the trained models, in a cluster. Several gligabao instances take requests behind one load balancer; each runs its own
// sargo with a local directory, and they share only the database and the bucket. So a training lives in the sargo of ONE instance, and
// the other instances must learn about it from the database:
//
//   - gb_model_train_jobs has a row per training. It is made (claimed) before sargo is asked, and the unique index of running rows per
//     user refuses a second training of the user on any instance. The owner instance follows its sargo and beats the row; when sargo is
//     done it copies the model into the store, records it in gb_trained_models (the table of the LoRA models of the sidecar, with
//     model_type "gcnii" and the user) and ends the row. A running row without a beat is marked lost by whoever looks next.
//   - the status of a training is read from that row, so every instance gives the same answer.
//   - before a model is used (status, infer, embed), the instance compares the model in its sargo directory (a marker file with the row
//     ID) with the latest row of the user and, if they differ, writes the latest one into its directory (Sync). Sargo reloads the file when
//     it changes.

// WeightsService is a Service that also keeps the weights of the models it trains; the sargo service of gligabao is one.
type WeightsService interface {
	Service
	Weights() *modeltrain.Weights
}

// Recorder tracks the trainings in the database and stores their models. A nil *Recorder does nothing (no store was configured).
type Recorder struct {
	svc     Service
	weights *modeltrain.Weights
	db      *sql.DB
	// instance identifies this process in gb_model_train_jobs.instance_id.
	instance string
}

// NewRecorder is the Recorder of svc over db, or nil if svc does not keep weights (not a WeightsService, or without a store) or there is
// no database.
func NewRecorder(svc Service,
	db *sql.DB) *Recorder {
	ws, ok := svc.(WeightsService)
	if !ok || db == nil {
		return nil
	}
	w := ws.Weights()
	if w == nil {
		return nil
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return &Recorder{
		svc:      svc,
		weights:  w,
		db:       db,
		instance: hex.EncodeToString(id),
	}
}

// Timing: how often the owner looks at its training (and beats the row), how long without a beat makes a running job lost, and how long
// a training is followed at the most.
var (
	watchEvery = 2 * time.Second
	beatStale  = 90 * time.Second
	watchMax   = 6 * time.Hour
)

// trainInfo is what the request that starts a training knows about it and sargo does not report; it is the "info" of the job row.
type trainInfo struct {
	Dataset string `json:"dataset,omitempty"`
	// Run is the ID of the record of the training in the history ("" if none).
	Run              string                 `json:"run,omitempty"`
	Params           modeltrain.TrainParams `json:"params"`
	Graphs           int                    `json:"graphs"`
	ValidationGraphs int                    `json:"validation_graphs"`
}

// Claim is a training this instance has claimed and has to follow.
type Claim struct {
	JobID string
	Info  trainInfo
}

// ErrBusy is the error of Claim when the user already has a running training (on this instance or another).
var ErrBusy = trainingdb.ErrModelTrainJobRunning

// Claim records, before sargo is asked to train, that this instance trains the model of the user: ErrBusy if the user has a running
// training anywhere in the cluster. A nil Recorder claims nothing and returns a nil *Claim.
func (r *Recorder) Claim(ctx context.Context,
	userID string,
	info trainInfo) (*Claim, error) {
	if r == nil {
		return nil, nil
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	infoJSON, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}
	c := &Claim{
		JobID: modeltrain.NewJobID(time.Now().Unix(), suffix),
		Info:  info,
	}
	_, err = trainingdb.ClaimModelTrainJob(ctx,
		r.db,
		trainingdb.ModelTrainJob{
			JobID:      c.JobID,
			ModelType:  trainingdb.ModelTypeGCNII,
			UserID:     userID,
			InstanceID: r.instance,
			Info:       infoJSON,
		},
		time.Now().Add(-beatStale))
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Abort ends the claim of a training that sargo refused to start.
func (r *Recorder) Abort(c *Claim,
	cause error) {
	if r == nil || c == nil {
		return
	}
	if err := trainingdb.EndModelTrainJob(context.WithoutCancel(context.Background()),
		r.db,
		c.JobID,
		trainingdb.ModelTrainJobFailed,
		cause.Error(),
		"{}",
		nil); err != nil {
		log.WithError(err).WithField("job_id", c.JobID).Error("model train: ending the claim of a refused training failed")
	}
}

// Watch follows the training claimed by c, whose sargo job is job, in the background until it ends: it beats the row while sargo trains
// and, when sargo is done, stores the weights, records the model and ends the row. It does not block. A nil Recorder or Claim does
// nothing.
func (r *Recorder) Watch(c *Claim,
	userID string,
	job sargo.GCNIIJob) {
	if r == nil || c == nil {
		return
	}
	go r.watch(context.Background(),
		watchEvery,
		watchMax,
		c,
		userID,
		job)
}

func (r *Recorder) watch(ctx context.Context,
	every,
	most time.Duration,
	c *Claim,
	userID string,
	started sargo.GCNIIJob) {
	l := log.WithFields(log.Fields{"user_id": userID, "job_id": c.JobID})
	end := func(status,
		errText string,
		job *sargo.GCNIIJob,
		modelID *int64) {
		raw := "{}"
		if job != nil {
			raw = jsonOf(job)
		}
		if err := trainingdb.EndModelTrainJob(context.WithoutCancel(ctx),
			r.db,
			c.JobID,
			status,
			errText,
			raw,
			modelID); err != nil {
			l.WithError(err).Error("model train: ending the job failed")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, most)
	defer cancel()
	// the beat tells the others the instance is alive even before the first look
	r.beat(ctx, c, &started)
	for {
		select {
		case <-ctx.Done():
			l.Warn("model train: gave up waiting for the training to end")
			end(trainingdb.ModelTrainJobLost, "the training took too long to follow", &started, nil)
			return
		case <-time.After(every):
		}
		st, err := r.svc.GCNIITrainStatus(ctx, userID)
		if err != nil {
			l.WithError(err).Warn("model train: reading the status failed, trying again") // no beat: sargo may be gone
			continue
		}
		job := st.Job
		switch {
		case job == nil || job.StartedUnix != started.StartedUnix:
			end(trainingdb.ModelTrainJobLost, "sargo lost the training (it restarted, or another training replaced it)", &started, nil)
			return
		case job.Status == sargo.GCNIIJobRunning:
			r.beat(ctx, c, job)
			continue
		case job.Status != sargo.GCNIIJobDone:
			end(trainingdb.ModelTrainJobFailed, job.Error, job, nil)
			return
		}
		id, err := r.record(context.WithoutCancel(ctx),
			c,
			userID,
			*job)
		if err != nil {
			l.WithError(err).Error("model train: storing the weights failed")
			end(trainingdb.ModelTrainJobFailed, "storing the weights failed: "+err.Error(), job, nil)
			return
		}
		end(trainingdb.ModelTrainJobDone, "", job, &id)
		l.WithField("model_id", id).Info("model train: the weights are stored and recorded")
		return
	}
}

func (r *Recorder) beat(ctx context.Context,
	c *Claim,
	job *sargo.GCNIIJob) {
	if _, err := trainingdb.BeatModelTrainJob(ctx,
		r.db,
		c.JobID,
		r.instance,
		jsonOf(job)); err != nil {
		log.WithError(err).WithField("job_id", c.JobID).Warn("model train: recording the beat failed")
	}
}

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// record stores the weights of the finished training of the user (job) and makes the row of the model; it returns its ID. The marker of
// the directory of sargo is written last: until then the job is still running and Sync leaves the directory alone.
func (r *Recorder) record(ctx context.Context,
	c *Claim,
	userID string,
	job sargo.GCNIIJob) (int64, error) {
	files, err := r.weights.Save(ctx,
		userID,
		c.JobID)
	if err != nil {
		return 0, err
	}
	params, err := json.Marshal(modeltrain.WeightsParams{
		Run:         c.Info.Run,
		Train:       c.Info.Params,
		Classes:     job.Classes,
		Accuracy:    job.Accuracy,
		Loss:        job.Loss,
		ValAccuracy: job.ValAccuracy,
		ValLoss:     job.ValLoss,
	})
	if err != nil {
		return 0, err
	}
	m, err := r.weights.NewModel(modeltrain.ModelInfo{
		UserID:           userID,
		JobID:            c.JobID,
		Dataset:          c.Info.Dataset,
		Graphs:           job.Graphs,
		ValidationGraphs: job.ValidationGraphs,
		Epochs:           job.Epochs,
		Loss:             job.Loss,
		Params:           params,
	},
		files)
	if err != nil {
		return 0, err
	}
	id, _, err := trainingdb.RecordTrainedModel(ctx,
		r.db,
		m)
	if err != nil {
		return 0, err
	}
	return id, r.weights.WriteMarker(userID, id)
}

// Sync makes the model in sargo's directory of the user the latest recorded one: if the marker of the directory is not the ID of the
// latest row (a model that another instance trained, or a directory that was lost with its machine) the files of that row are written
// back. It does nothing while this instance trains for the user (the directory then holds the newest model, not yet recorded), when the
// user has no recorded model, and for a nil Recorder. It says whether it restored.
func (r *Recorder) Sync(ctx context.Context,
	userID string) (bool, error) {
	if r == nil {
		return false, nil
	}
	job, err := r.latestJob(ctx, userID)
	if err != nil {
		return false, err
	}
	if job != nil && job.Status == trainingdb.ModelTrainJobRunning && job.InstanceID == r.instance {
		return false, nil
	}
	models, err := trainingdb.ListUserTrainedModels(ctx,
		r.db,
		trainingdb.ModelTypeGCNII,
		userID,
		1)
	if err != nil || len(models) == 0 || r.weights.Marker(userID) == models[0].ID {
		return false, err
	}
	if err := r.weights.Restore(ctx, models[0]); err != nil {
		return false, err
	}
	log.WithFields(log.Fields{"user_id": userID, "model_id": models[0].ID}).Info("model: the latest recorded model was written to the directory of sargo")
	return true, nil
}

// latestJob is the newest training of the user, after marking the running ones that stopped beating as lost; nil if there is none.
func (r *Recorder) latestJob(ctx context.Context,
	userID string) (*trainingdb.ModelTrainJob, error) {
	if _, err := trainingdb.ExpireModelTrainJobs(ctx,
		r.db,
		trainingdb.ModelTypeGCNII,
		userID,
		time.Now().Add(-beatStale)); err != nil {
		return nil, err
	}
	job, err := trainingdb.LatestModelTrainJob(ctx,
		r.db,
		trainingdb.ModelTypeGCNII,
		userID)
	if errors.Is(err, trainingdb.ErrNotFound) {
		return nil, nil
	}
	return job, err
}

// JobOf is the job to show for the user: the newest training of the cluster as the database knows it, so every instance gives the same
// answer (a running job is running until its weights are recorded; a lost one is failed). local is what this instance's sargo says; it is
// used when the database knows no job, or a newer one is only in sargo (a training from before the table).
func (r *Recorder) JobOf(ctx context.Context,
	userID string,
	local *sargo.GCNIIJob) *sargo.GCNIIJob {
	if r == nil {
		return local
	}
	row, err := r.latestJob(ctx, userID)
	if err != nil {
		log.WithError(err).WithField("user_id", userID).Warn("model: reading the latest training failed, showing sargo's")
		return local
	}
	if row == nil {
		return local
	}
	return mergeJob(row, local)
}

// mergeJob is the sargo job of a row, with the status and error of the row; local wins if it is a newer training than the row's.
func mergeJob(row *trainingdb.ModelTrainJob,
	local *sargo.GCNIIJob) *sargo.GCNIIJob {
	var job sargo.GCNIIJob
	_ = json.Unmarshal(row.Job, &job)
	if job.StartedUnix == 0 {
		job.StartedUnix = row.StartedAt.Unix()
	}
	if local != nil && local.StartedUnix > job.StartedUnix {
		return local
	}
	switch row.Status {
	case trainingdb.ModelTrainJobDone:
		job.Status = sargo.GCNIIJobDone
	case trainingdb.ModelTrainJobFailed, trainingdb.ModelTrainJobLost:
		job.Status = sargo.GCNIIJobFailed
		job.Error = row.Error
	default:
		job.Status = sargo.GCNIIJobRunning
	}
	if row.FinishedAt != nil && job.FinishedUnix == 0 {
		job.FinishedUnix = row.FinishedAt.Unix()
	}
	return &job
}

// ListModels serves GET /v1/gb/model/models?model_type=gcnii: the trained models of the user of the request (rows of gb_trained_models,
// the same as the LoRA models have), newest first: {"models": [...]}. It lists what was stored; sargo uses the latest one. Empty without a
// store of the weights.
func ListModels(rec *Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := user(w, r)
		if !ok {
			return
		}
		if !checkType(w, r.URL.Query().Get("model_type")) {
			return
		}
		models := []trainingdb.TrainedModel{}
		if rec != nil {
			var err error
			models, err = trainingdb.ListUserTrainedModels(r.Context(),
				rec.db,
				trainingdb.ModelTypeGCNII,
				userID,
				0)
			if err != nil {
				log.WithError(err).WithField("user_id", userID).Error("model list failed")
				httputil.Error(w,
					http.StatusInternalServerError,
					"failed to list the models")
				return
			}
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			map[string]any{"models": models})
	}
}
