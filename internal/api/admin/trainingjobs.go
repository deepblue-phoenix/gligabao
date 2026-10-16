package admin

// Training jobs in the admin UI: start a training job on the sidecar and record it (gb_training_job_runs).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
	log "github.com/sirupsen/logrus"
)

// TrainingJobStarter starts training jobs on the sidecar; *sidecar.Client is one.
type TrainingJobStarter interface {
	StartTrainingJob(ctx context.Context, req sidecar.StartTrainingJobRequest) (sidecar.TrainingJob, error)
}

// maxTrainingJobBody bounds the request body of StartTrainingJobOf.
const maxTrainingJobBody = 1 << 16

// StartTrainingJobOf serves POST /admin/api/training/jobs[?db=<name>] with an optional JSON body
// {"num_train_steps": 100, "model_size": "4b"}: it starts a training job on the sidecar through sc (the sidecar's REST API)
// and stores the TrainingJob the sidecar reports in the gb_training_job_runs table of one of dbs (the first without db).
// It answers 201 with the stored record (relational.TrainingJobRun). A nil sc (no sidecar) answers 503, a sidecar error 502.
func StartTrainingJobOf(dbs []Named, sc TrainingJobStarter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		if sc == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "sidecar is not configured")
			return
		}
		var req sidecar.StartTrainingJobRequest
		body := http.MaxBytesReader(w, r.Body, maxTrainingJobBody)
		if err := json.NewDecoder(body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		req.ModelSize = strings.ToLower(strings.TrimSpace(req.ModelSize))
		if req.NumTrainSteps < 0 {
			httputil.Error(w, http.StatusBadRequest, "num_train_steps must be positive")
			return
		}
		switch req.ModelSize {
		case "", sidecar.ModelSize4B, sidecar.ModelSize12B:
		default:
			httputil.Error(w, http.StatusBadRequest, "model_size must be \"4b\" or \"12b\"")
			return
		}

		ctx := r.Context()
		job, err := sc.StartTrainingJob(ctx, req)
		if err != nil {
			log.WithError(err).Error("admin: start training job failed")
			httputil.Error(w, http.StatusBadGateway, "sidecar failed to start the training job")
			return
		}
		// The job is running now: record it even if the client went away meanwhile.
		ctx = context.WithoutCancel(ctx)
		id, err := relational.RecordTrainingJobRun(ctx, n.DB, relational.TrainingJobRun{
			JobID: job.JobID, Status: job.Status, ModelSize: job.ModelSize, NumTrainSteps: job.NumTrainSteps, StartedAt: job.StartedAt})
		if err != nil {
			log.WithError(err).WithField("job_id", job.JobID).Error("admin: record training job run failed")
			httputil.Error(w, http.StatusInternalServerError, "training job "+job.JobID+" started but could not be recorded")
			return
		}
		run, err := relational.GetTrainingJobRun(ctx, n.DB, id)
		if err != nil {
			log.WithError(err).WithField("job_id", job.JobID).Error("admin: read training job run failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the training job run")
			return
		}
		httputil.WriteJSON(w, http.StatusCreated, run)
	}
}
