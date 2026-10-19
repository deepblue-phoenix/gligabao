package training

// Training jobs: start a training job on the sidecar and record it (gb_training_job_runs), list the jobs,
// and stream the jobs' progress (see Monitor).

import (
	"context"
	"encoding/base64"
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

// maxTrainingJobBody bounds the request body of StartJobOf.
const maxTrainingJobBody = 1 << 16

// The dataset a job names is one of the training datasets directory (manifest.go; also what UploadDatasetOf adds to it), by its
// name; its data file is sent to the sidecar in the request that starts the job (the sidecar has no dataset
// endpoints). A job needs a dataset: no name, or a name not in the directory, answers 400.
//
// StartJobOf serves POST /v1/gb/training/jobs[?db=<name>] with a JSON body
// {"dataset": "my-dataset", "num_train_steps": 100, "model_size": "4b"} (only dataset is required): it starts a training job on the sidecar through sc
// (the sidecar's REST API) and stores the TrainingJob the sidecar reports in the gb_training_job_runs table of one of dbs
// (the first without db), then has mon (may be nil) follow the job's progress. It answers 201 with the stored record
// (trainingdb.TrainingJobRun). A nil sc (no sidecar) answers 503, a sidecar error 502, except that a request the sidecar
// rejects as invalid answers 400 with its message.
func StartJobOf(dbs []Database, sc JobStarter, mon *Monitor) http.HandlerFunc {
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
		// Only these fields come from the client; the dataset's content is added from the dataset's file.
		var in struct {
			NumTrainSteps int    `json:"num_train_steps"`
			ModelSize     string `json:"model_size"`
			Dataset       string `json:"dataset"`
		}
		body := http.MaxBytesReader(w, r.Body, maxTrainingJobBody)
		if err := json.NewDecoder(body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		req := sidecar.StartTrainingJobRequest{NumTrainSteps: in.NumTrainSteps,
			ModelSize: strings.ToLower(strings.TrimSpace(in.ModelSize))}
		name := strings.TrimSpace(in.Dataset)
		if name == "" {
			httputil.Error(w, http.StatusBadRequest, "dataset is required: the name of a training dataset")
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
		dataset, content, err := loadTrainingDataset(ctx, n, name)
		if errors.Is(err, errUnknownDataset) {
			httputil.Error(w, http.StatusBadRequest, "unknown dataset "+name+": there is no dataset of that name in the training datasets directory")
			return
		} else if err != nil {
			httputil.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		req.Dataset, req.DatasetContent = dataset.Filename, base64.StdEncoding.EncodeToString(content)
		job, err := sc.StartTrainingJob(ctx, req)
		if err != nil {
			var se *sidecar.StatusError
			if errors.As(err, &se) && se.Code == http.StatusBadRequest {
				httputil.Error(w, http.StatusBadRequest, se.Message())
				return
			}
			log.WithError(err).Error("training: start training job failed")
			httputil.Error(w, http.StatusBadGateway, "sidecar failed to start the training job")
			return
		}
		// The job is running now: record it even if the client went away meanwhile.
		ctx = context.WithoutCancel(ctx)
		// the sidecar has taken this version of the file
		if _, err := trainingdb.MarkTrainingDatasetSynced(ctx, n.DB, dataset.ID, dataset.SHA256); err != nil {
			log.WithError(err).WithField("filename", dataset.Filename).Error("training: mark training dataset synced failed")
		}
		id, err := trainingdb.RecordTrainingJobRun(ctx, n.DB, trainingdb.TrainingJobRun{
			JobID: job.JobID, ProviderJobID: job.ProviderJobID, Status: job.Status, ModelSize: job.ModelSize, Dataset: job.Dataset,
			NumTrainSteps: job.NumTrainSteps, StartedAt: job.StartedAt})
		if err != nil {
			log.WithError(err).WithField("job_id", job.JobID).Error("training: record training job run failed")
			httputil.Error(w, http.StatusInternalServerError, "training job "+job.JobID+" started but could not be recorded")
			return
		}
		run, err := trainingdb.GetTrainingJobRun(ctx, n.DB, id)
		if err != nil {
			log.WithError(err).WithField("job_id", job.JobID).Error("training: read training job run failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the training job run")
			return
		}
		mon.Watch(n, *run)
		httputil.WriteJSON(w, http.StatusCreated, run)
	}
}

// StopJobOf serves POST /v1/gb/training/jobs/{id}/stop[?db=<name>]: it asks the sidecar to stop the job run {id}. The
// job ends at its next step (or at the next stage of its setup) with a "cancelled" event, which the monitor records like
// any other: the answer is 202 with {"id": <run>, "status": "stopping"}, and the job's row changes when the event arrives.
// 404 is for an unknown run, 409 for one that is over already, 503 for a nil sc, 502 for a sidecar error.
func StopJobOf(dbs []Database, sc JobCanceller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			httputil.Error(w, http.StatusBadRequest, "id must be a positive integer")
			return
		}
		if sc == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "sidecar is not active")
			return
		}
		run, err := trainingdb.GetTrainingJobRun(r.Context(), n.DB, id)
		if errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown training job run")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read training job run failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the training job run")
			return
		}
		if trainingdb.TrainingJobFinished(run.Status) {
			httputil.Error(w, http.StatusConflict, "the training job is over already ("+run.Status+")")
			return
		}
		if err := sc.CancelTrainingJob(r.Context(), run.JobID); err != nil {
			var se *sidecar.StatusError
			if errors.As(err, &se) && se.Code == http.StatusConflict {
				httputil.Error(w, http.StatusConflict, se.Message())
				return
			}
			log.WithError(err).WithField("job_id", run.JobID).Error("training: stop training job failed")
			httputil.Error(w, http.StatusBadGateway, "sidecar failed to stop the training job")
			return
		}
		log.WithFields(log.Fields{"db": n.Name, "run_id": run.ID, "job_id": run.JobID}).Info("training: stop requested")
		httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"id": run.ID, "status": "stopping"})
	}
}

const (
	trainingJobsDefaultLimit = 50
	trainingJobsMaxLimit     = 500
	trainingKeepalive        = 15 * time.Second
)

// ListJobsOf serves GET /v1/gb/training/jobs[?db=<name>][&limit=<n>]: {"jobs": [...]}, the training job runs
// of one of dbs (the first without db), newest first, at most limit (default 50, at most 500).
func ListJobsOf(dbs []Database) http.HandlerFunc {
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
		runs, err := trainingdb.ListTrainingJobRuns(r.Context(), n.DB, limit, false)
		if err != nil {
			log.WithError(err).Error("training: list training jobs failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the training jobs")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"jobs": runs})
	}
}

// EventsOf serves GET /v1/gb/training/events[?db=<name>][&run_id=<id>] as Server-Sent Events, the feed of the
// events the sidecar reports for training jobs (forwarded by mon): `event: status` or `event: progress`, `data:` a
// FeedEvent as JSON.
//
// With run_id the stream is of that run: first the events already stored (from the one after Last-Event-ID, which a
// reconnecting EventSource sends, or from the `from` parameter), then the live ones; each has `id: <seq>`. The stream
// stays open after the run's last event; the client closes it. Without run_id it is the live events of all the runs of the
// database, to follow the list of jobs (no history, no ids).
func EventsOf(dbs []Database, mon *Monitor) http.HandlerFunc {
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
		var run *trainingdb.TrainingJobRun
		var history []trainingdb.TrainingJobEvent
		if runID != 0 {
			var err error
			if run, err = trainingdb.GetTrainingJobRun(r.Context(), n.DB, runID); errors.Is(err, relational.ErrNotFound) {
				httputil.Error(w, http.StatusNotFound, "unknown training job run")
				return
			} else if err != nil {
				log.WithError(err).Error("training: read training job run failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to read the training job run")
				return
			}
			if history, err = trainingdb.ListTrainingJobEvents(r.Context(), n.DB, runID, from); err != nil {
				log.WithError(err).Error("training: read training job events failed")
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
		send := func(ev FeedEvent) bool {
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
			if !send(FeedEvent{DB: n.Name, RunID: runID, JobID: run.JobID, TrainingEvent: ev}) {
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
