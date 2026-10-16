package admin

// Training jobs in the admin UI: start a training job on the sidecar and record it (gb_training_job_runs), list the jobs
// and the sidecar's training datasets, and stream the jobs' progress (see TrainingMonitor).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
// {"num_train_steps": 100, "model_size": "4b", "dataset": "file.jsonl"}: it starts a training job on the sidecar through sc
// (the sidecar's REST API) and stores the TrainingJob the sidecar reports in the gb_training_job_runs table of one of dbs
// (the first without db), then has mon (may be nil) follow the job's progress. It answers 201 with the stored record
// (relational.TrainingJobRun). A nil sc (no sidecar) answers 503, a sidecar error 502, except that a request the sidecar
// rejects as invalid (e.g. an unknown dataset) answers 400 with its message.
func StartTrainingJobOf(dbs []Named, sc TrainingJobStarter, mon *TrainingMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		if sc == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "sidecar is not active")
			return
		}
		var req sidecar.StartTrainingJobRequest
		body := http.MaxBytesReader(w, r.Body, maxTrainingJobBody)
		if err := json.NewDecoder(body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		req.ModelSize = strings.ToLower(strings.TrimSpace(req.ModelSize))
		req.Dataset = strings.TrimSpace(req.Dataset)
		if strings.ContainsAny(req.Dataset, `/\`) || (req.Dataset != "" && !strings.HasSuffix(req.Dataset, ".jsonl")) {
			httputil.Error(w, http.StatusBadRequest, "dataset must be the file name of a training dataset (*.jsonl)")
			return
		}
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
			var se *sidecar.StatusError
			if errors.As(err, &se) && se.Code == http.StatusBadRequest {
				httputil.Error(w, http.StatusBadRequest, se.Message())
				return
			}
			log.WithError(err).Error("admin: start training job failed")
			httputil.Error(w, http.StatusBadGateway, "sidecar failed to start the training job")
			return
		}
		// The job is running now: record it even if the client went away meanwhile.
		ctx = context.WithoutCancel(ctx)
		id, err := relational.RecordTrainingJobRun(ctx, n.DB, relational.TrainingJobRun{
			JobID: job.JobID, ProviderJobID: job.ProviderJobID, Status: job.Status, ModelSize: job.ModelSize, Dataset: job.Dataset,
			NumTrainSteps: job.NumTrainSteps, StartedAt: job.StartedAt})
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
		mon.Watch(n, *run)
		httputil.WriteJSON(w, http.StatusCreated, run)
	}
}

const (
	trainingJobsDefaultLimit = 50
	trainingJobsMaxLimit     = 500
	trainingKeepalive        = 15 * time.Second
)

// ListTrainingJobsOf serves GET /admin/api/training/jobs[?db=<name>][&limit=<n>]: {"jobs": [...]}, the training job runs
// of one of dbs (the first without db), newest first, at most limit (default 50, at most 500).
func ListTrainingJobsOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pick(dbs, q.Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		limit := trainingJobsDefaultLimit
		if s := q.Get("limit"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil || v < 1 {
				httputil.Error(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			limit = min(v, trainingJobsMaxLimit)
		}
		runs, err := relational.ListTrainingJobRuns(r.Context(), n.DB, limit, false)
		if err != nil {
			log.WithError(err).Error("admin: list training jobs failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the training jobs")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"jobs": runs})
	}
}

// TrainingStatusOf serves GET /admin/api/training/status: {"sidecar_active": bool}, whether the sidecar is usable (a nil sc
// is one that was not active on startup). The training view shows a notice when it is not.
func TrainingStatusOf(sc TrainingSidecar) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"sidecar_active": sc != nil})
	}
}

// TrainingDatasetsOf serves GET /admin/api/training/datasets: {"datasets": [...]}, the training datasets on the sidecar
// (what a training job can be started on). A nil sc answers 503, a sidecar error 502.
func TrainingDatasetsOf(sc TrainingSidecar) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sc == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "sidecar is not active")
			return
		}
		ds, err := sc.ListTrainingDatasets(r.Context())
		if err != nil {
			log.WithError(err).Error("admin: list training datasets failed")
			httputil.Error(w, http.StatusBadGateway, "sidecar failed to list the training datasets")
			return
		}
		if ds == nil {
			ds = []sidecar.TrainingDataset{}
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"datasets": ds})
	}
}

// TrainingEventsOf serves GET /admin/api/training/events[?db=<name>][&run_id=<id>] as Server-Sent Events, the feed of the
// events the sidecar reports for training jobs (forwarded by mon): `event: status` or `event: progress`, `data:` a
// TrainingFeedEvent as JSON.
//
// With run_id the stream is of that run: first the events already stored (from the one after Last-Event-ID, which a
// reconnecting EventSource sends, or from the `from` parameter), then the live ones; each has `id: <seq>`. The stream
// stays open after the run's last event; the client closes it. Without run_id it is the live events of all the runs of the
// database, to follow the list of jobs (no history, no ids).
func TrainingEventsOf(dbs []Named, mon *TrainingMonitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pick(dbs, q.Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		if mon == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "training is not available")
			return
		}
		var runID int64
		if s := q.Get("run_id"); s != "" {
			v, err := strconv.ParseInt(s, 10, 64)
			if err != nil || v < 1 {
				httputil.Error(w, http.StatusBadRequest, "run_id must be a positive integer")
				return
			}
			runID = v
		}
		from := 0
		if s := r.Header.Get("Last-Event-ID"); s != "" {
			if v, err := strconv.Atoi(s); err == nil && v >= 0 {
				from = v + 1
			}
		} else if s := q.Get("from"); s != "" {
			if v, err := strconv.Atoi(s); err == nil && v >= 0 {
				from = v
			}
		}

		// Subscribe before reading the stored events, so none falls between the two; the overlap is skipped by seq.
		sub, cancel := mon.subscribe(n.Name, runID)
		defer cancel()
		var run *relational.TrainingJobRun
		var history []relational.TrainingJobEvent
		if runID != 0 {
			var err error
			if run, err = relational.GetTrainingJobRun(r.Context(), n.DB, runID); errors.Is(err, relational.ErrNotFound) {
				httputil.Error(w, http.StatusNotFound, "unknown training job run")
				return
			} else if err != nil {
				log.WithError(err).Error("admin: read training job run failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to read the training job run")
				return
			}
			if history, err = relational.ListTrainingJobEvents(r.Context(), n.DB, runID, from); err != nil {
				log.WithError(err).Error("admin: read training job events failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to read the training job events")
				return
			}
		}

		rc := http.NewResponseController(w)
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		send := func(ev TrainingFeedEvent) bool {
			b, err := json.Marshal(ev)
			if err != nil {
				return true
			}
			if runID != 0 {
				_, err = w.Write([]byte("id: " + strconv.Itoa(ev.Seq) + "\n"))
			}
			if err == nil {
				_, err = w.Write([]byte("event: " + ev.Type + "\ndata: " + string(b) + "\n\n"))
			}
			return err == nil && rc.Flush() == nil
		}
		if _, err := w.Write([]byte(": connected\n\n")); err != nil || rc.Flush() != nil {
			return
		}
		next := from
		for _, e := range history {
			var ev sidecar.TrainingEvent
			if json.Unmarshal([]byte(e.Data), &ev) != nil {
				continue
			}
			ev.Seq = e.Seq
			if !send(TrainingFeedEvent{DB: n.Name, RunID: runID, JobID: run.JobID, TrainingEvent: ev}) {
				return
			}
			next = e.Seq + 1
		}

		keepalive := time.NewTicker(trainingKeepalive)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-mon.ctx.Done():
				return
			case ev, ok := <-sub.ch:
				if !ok { // fell behind: the client reconnects and is caught up from the database
					return
				}
				if runID != 0 && ev.Seq < next {
					continue
				}
				if !send(ev) {
					return
				}
				next = ev.Seq + 1
			case <-keepalive.C:
				if _, err := w.Write([]byte(": keepalive\n\n")); err != nil || rc.Flush() != nil {
					return
				}
			}
		}
	}
}
