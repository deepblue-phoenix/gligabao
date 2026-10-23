package training

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

// maxTrainingJobBody bounds the request body of StartJobOf.
const maxTrainingJobBody = 1 << 16

// The dataset a job names is one of the training datasets directory (internal/training/manifest.go; also what UploadDatasetOf adds to it), by its
// name; its data file is sent to the sidecar in the request that starts the job (the sidecar has no dataset
// endpoints). A job needs a dataset: no name, or a name not in the directory, answers 400.
//
// StartJobOf serves POST /v1/gb/training/jobs with a JSON body
// {"dataset": "my-dataset", "num_train_steps": 100, "model_size": "4b"} (only dataset is required): it starts a training job on the sidecar through sc
// (the sidecar's REST API) and stores the TrainingJob the sidecar reports in the gb_training_job_runs table
// (the first without db), then has mon (may be nil) follow the job's progress. It answers 201 with the stored record
// (trainingdb.TrainingJobRun). A nil sc (no sidecar) answers 503, a sidecar error 502, except that a request the sidecar
// rejects as invalid answers 400 with its message.
func StartJobOf(db *sql.DB, datasetDirs []string, sc core.JobStarter, mon *core.Monitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		run, err := core.StartJob(r.Context(), db, datasetDirs, sc, mon, core.StartJobRequest{NumTrainSteps: in.NumTrainSteps, ModelSize: in.ModelSize, Dataset: in.Dataset})
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusCreated, run)
	}
}

// StopJobOf serves POST /v1/gb/training/jobs/{id}/stop: it asks the sidecar to stop the job run {id}. The
// job ends at its next step (or at the next stage of its setup) with a "cancelled" event, which the monitor records like
// any other: the answer is 202 with {"id": <run>, "status": "stopping"}, and the job's row changes when the event arrives.
// 404 is for an unknown run, 409 for one that is over already, 503 for a nil sc, 502 for a sidecar error.
func StopJobOf(db *sql.DB, sc core.JobCanceller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if sc == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "sidecar is not active")
			return
		}
		run, err := core.StopJob(r.Context(), db, sc, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"id": run.ID, "status": "stopping"})
	}
}

const (
	trainingJobsDefaultLimit = 50
	trainingJobsMaxLimit     = 500
	trainingKeepalive        = 15 * time.Second
)

// ListJobsOf serves GET /v1/gb/training/jobs[?limit=<n>]: {"jobs": [...]}, the training job runs
// , newest first, at most limit (default 50, at most 500).
func ListJobsOf(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, ok := limitParam(w, r, trainingJobsDefaultLimit, trainingJobsMaxLimit)
		if !ok {
			return
		}
		runs, err := trainingdb.ListTrainingJobRuns(r.Context(), db, limit, false)
		if err != nil {
			log.WithError(err).Error("training: list training jobs failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the training jobs")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"jobs": runs})
	}
}

// EventsOf serves GET /v1/gb/training/events[?run_id=<id>] as Server-Sent Events, the feed of the
// events the sidecar reports for training jobs (forwarded by mon): `event: status`, `event: progress` or `event: log` (what
// the job is doing: loading the model, ...), `data:` a FeedEvent as JSON.
//
// With run_id the stream is of that run: first the events already stored (from the one after Last-Event-ID, which a
// reconnecting EventSource sends, or from the `from` parameter), then the live ones; each has `id: <seq>`. The stream
// stays open after the run's last event; the client closes it. Without run_id it is the live events of all the runs of the
// database, to follow the list of jobs (no history, no ids).
func EventsOf(db *sql.DB, mon *core.Monitor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
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
		sub, cancel := mon.Subscribe(runID)
		defer cancel()
		var history []core.FeedEvent
		if runID != 0 {
			var err error
			if history, err = core.JobHistory(r.Context(), db, runID, from); err != nil {
				writeErr(w, err)
				return
			}
		}

		rc := http.NewResponseController(w)
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		send := func(ev core.FeedEvent) bool {
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
		for _, ev := range history {
			if !send(ev) {
				return
			}
			next = ev.Seq + 1
		}

		keepalive := time.NewTicker(trainingKeepalive)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-mon.Done():
				return
			case ev, ok := <-sub.C:
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
