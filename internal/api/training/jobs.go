package training

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

// maxTrainingJobBody bounds the request body of StartJobOf (a GCNII* job may bring its graphs: it is also sargo's limit for one request).
const maxTrainingJobBody = 10<<20 - 4<<10

// StartJobOf serves POST /v1/gb/training/jobs: it starts a training job (of any model type: this is the one handler, and the pipeline behind it
// the one of every kind of training) and stores the run in the gb_training_job_runs table (the first without db), then has mon (may be nil)
// follow the job's progress. The body is JSON with "model_type" ("lora", the default, or "gcnii") and the fields of that type:
//
//	lora:  {"dataset": "my-dataset", "num_train_steps": 100, "model_size": "4b"} (only dataset is required). The dataset is one of the training
//	       datasets directory (internal/training/manifest.go; also what UploadDatasetOf adds to it), by its name; its data file is sent to the
//	       ML sidecar in the request that starts the job. A job needs a dataset: no name, or a name not in the directory, answers 400.
//	gcnii: {"dataset"?, "classes"?, "min_graphs_per_class"?, "validation_fraction"?, "split_seed"?, "epochs"?, "lr"?, ...} (see gcnii.Params):
//	       trains the model of the user on the graphs of a dataset with the classes an LLM gave them, or on the labelled "graphs" of the body.
//
// It answers 201 with the stored record (trainingdb.TrainingJobRun). A model type whose backend is not on (no ML sidecar, no sargo) answers
// 503, a bad request 400, a user that is training a GCNII* model already 409, a backend service error 502.
func StartJobOf(db *sql.DB,
	backends core.Backends,
	mon *core.Monitor) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := userOf(w, r)
		if !ok {
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w,
			r.Body,
			maxTrainingJobBody))
		if err != nil {
			httputil.Error(w,
				http.StatusBadRequest,
				"invalid body")
			return
		}
		var head struct {
			ModelType string `json:"model_type"`
		}
		if len(raw) != 0 && json.Unmarshal(raw, &head) != nil {
			httputil.Error(w,
				http.StatusBadRequest,
				"invalid JSON body")
			return
		}
		run, err := core.StartJob(r.Context(),
			db,
			backends,
			mon,
			core.StartJobRequest{
				UserID:    userID,
				ModelType: head.ModelType,
				Params:    raw,
			})
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusCreated,
			run)
	}
}

// StopJobOf serves POST /v1/gb/training/jobs/{id}/stop: it asks the backend of the job run {id} (the ML sidecar, sargo) to stop it. The
// job ends at its next step (or at the next stage of its setup) with a "cancelled" event, which the monitor records like
// any other: the answer is 202 with {"id": <run>, "status": "stopping"}, and the job's row changes when the event arrives.
// 404 is for an unknown run, 409 for one that is over already, 503 for a backend that is not on, 502 for a backend service error.
func StopJobOf(db *sql.DB,
	backends core.Backends,
	mon *core.Monitor) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := userOf(w, r)
		if !ok {
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, err := core.StopJob(r.Context(),
			db,
			backends,
			mon,
			userID,
			id)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w,
			http.StatusAccepted,
			map[string]any{"id": run.ID, "status": "stopping"})
	}
}

const (
	trainingJobsDefaultLimit = 50
	trainingJobsMaxLimit     = 500
	trainingKeepalive        = 15 * time.Second
)

// ListJobsOf serves GET /v1/gb/training/jobs[?limit=<n>][&model_type=<type>]: {"jobs": [...]}, the training job runs of the user of the
// request (of every model type, or only the one of model_type), newest first, at most limit (default 50, at most 500). The runs whose
// instance stopped are marked failed first (Backend.Reap).
func ListJobsOf(db *sql.DB,
	backends core.Backends) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		limit, ok := limitParam(w,
			r,
			trainingJobsDefaultLimit,
			trainingJobsMaxLimit)
		if !ok {
			return
		}
		userID, ok := userOf(w, r)
		if !ok {
			return
		}
		modelType := r.URL.Query().Get("model_type")
		for _, b := range backends {
			if err := b.Reap(r.Context(), userID); err != nil {
				log.WithError(err).WithField("user_id", userID).Warn("training: reaping the runs of a stopped instance failed")
			}
		}
		runs, err := trainingdb.ListUserTrainingJobRuns(r.Context(),
			db,
			userID,
			0)
		if err == nil {
			runs = jobsOf(runs, modelType, limit)
		}
		if err != nil {
			log.WithError(err).Error("training: list training jobs failed")
			httputil.Error(w,
				http.StatusInternalServerError,
				"failed to list the training jobs")
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			map[string]any{"jobs": runs})
	}
}

// EventsOf serves GET /v1/gb/training/events[?run_id=<id>] as Server-Sent Events, the feed of the
// training events of the jobs of every model type (forwarded by mon), the same ones the Hooks.TrainingEvents callbacks get. `event:` is the type of the event
// (monitoring.TrainingEvent.Type): `status` and `progress` from every backend (a GCNII* job reports one progress per epoch), `log` (what the
// job is doing: loading the model, ...) and `system` (of the job's container) from the ML sidecar, and `started`, `stop_requested`, `model_recorded` and `stream_error` from gligabao itself; `data:` is the
// monitoring.TrainingEvent as JSON. The events of gligabao itself are live only: their seq is -1 (monitoring.NoSeq), they have no `id:` and
// they are not replayed.
//
// With run_id the stream is of that run: first the events already stored (from the one after Last-Event-ID, which a
// reconnecting EventSource sends, or from the `from` parameter), then the live ones; each has `id: <seq>`. The stream
// stays open after the run's last event; the client closes it. Without run_id it is the live events of all the runs of the
// user, to follow the list of jobs (no history, no ids).
func EventsOf(db *sql.DB,
	mon *core.Monitor) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID, ok := userOf(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		if mon == nil {
			httputil.Error(w,
				http.StatusServiceUnavailable,
				"training is not available")
			return
		}
		var runID int64
		if s := q.Get("run_id"); s != "" {
			v, err := strconv.ParseInt(s,
				10,
				64)
			if err != nil || v < 1 {
				httputil.Error(w,
					http.StatusBadRequest,
					"run_id must be a positive integer")
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
		sub, cancel := mon.Subscribe(runID,
			userID)
		defer cancel()
		var history []monitoring.TrainingEvent
		if runID != 0 {
			var err error
			if history, err = core.JobHistory(r.Context(),
				db,
				userID,
				runID,
				from); err != nil {
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
		send := func(ev monitoring.TrainingEvent) bool {
			b, err := json.Marshal(ev)
			if err != nil {
				return true
			}
			if runID != 0 && ev.Seq != monitoring.NoSeq {
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
				if runID != 0 && ev.Seq != monitoring.NoSeq && ev.Seq < next {
					continue
				}
				if !send(ev) {
					return
				}
				if ev.Seq != monitoring.NoSeq {
					next = ev.Seq + 1
				}
			case <-keepalive.C:
				if _, err := w.Write([]byte(": keepalive\n\n")); err != nil || rc.Flush() != nil {
					return
				}
			}
		}
	}
}

// jobsOf are the runs of the model type ("": all), at most limit of them, in the order they are given.
func jobsOf(runs []trainingdb.TrainingJobRun,
	modelType string,
	limit int) []trainingdb.TrainingJobRun {
	out := make([]trainingdb.TrainingJobRun, 0, min(limit, len(runs)))
	for _, r := range runs {
		if (modelType == "" || r.Type() == modelType) && len(out) < limit {
			out = append(out, r)
		}
	}
	return out
}
