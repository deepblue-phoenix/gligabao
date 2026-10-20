// Package api is the HTTP side of the evals of trained models (package training, its Manager): the handlers that start, list, read,
// cancel and watch the evals, list the evals the server can run, and list the training experiments. They are made over a
// *training.Manager, and Register puts them on a mux under a path prefix (the server picks it: "/v1/gb/training" gives the paths of
// the doc comments below). The records are served as the views of package training (RunView, CaseView, ...), which the admin UI reads.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/training"
	log "github.com/sirupsen/logrus"
)

const (
	maxStartBody  = 1 << 14
	defaultLimit  = 50
	maxLimit      = 500
	keepaliveTime = 15 * time.Second
)

// Handlers are the endpoints of the evals of trained models.
type Handlers struct {
	m *training.Manager
}

// New makes the handlers over m.
func New(m *training.Manager) *Handlers { return &Handlers{m: m} }

// Register puts the handlers on mux, under prefix ("" or e.g. "/v1/gb/training"):
//
//	GET  {prefix}/eval-definitions          DefinitionsOf
//	POST {prefix}/models/{id}/evals         StartOf
//	GET  {prefix}/models/{id}/evals         ListOf
//	GET  {prefix}/evals/{id}                GetOf
//	GET  {prefix}/evals/{id}/events         EventsOf
//	POST {prefix}/evals/{id}/cancel         CancelOf
//	GET  {prefix}/experiments               ExperimentsOf
//	GET  {prefix}/experiments/{id}          ExperimentOf
func (h *Handlers) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix+"/eval-definitions", h.DefinitionsOf())
	mux.HandleFunc("POST "+prefix+"/models/{id}/evals", h.StartOf())
	mux.HandleFunc("GET "+prefix+"/models/{id}/evals", h.ListOf())
	mux.HandleFunc("GET "+prefix+"/evals/{id}", h.GetOf())
	mux.HandleFunc("GET "+prefix+"/evals/{id}/events", h.EventsOf())
	mux.HandleFunc("POST "+prefix+"/evals/{id}/cancel", h.CancelOf())
	mux.HandleFunc("GET "+prefix+"/experiments", h.ExperimentsOf())
	mux.HandleFunc("GET "+prefix+"/experiments/{id}", h.ExperimentOf())
}

// writeErr answers with the status of the Kind of err (500 for any other error).
func writeErr(w http.ResponseWriter, err error) {
	var e *training.Error
	if !errors.As(err, &e) {
		httputil.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	status := http.StatusInternalServerError
	switch e.Kind {
	case training.KindInvalid:
		status = http.StatusBadRequest
	case training.KindNotFound:
		status = http.StatusNotFound
	case training.KindConflict:
		status = http.StatusConflict
	case training.KindUnavailable:
		status = http.StatusServiceUnavailable
	}
	httputil.Error(w, status, e.Msg)
}

// pathID reads the {id} of the path, answering 400 itself if it is not a positive integer.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		httputil.Error(w, http.StatusBadRequest, "id must be a positive integer")
		return 0, false
	}
	return id, true
}

// limitParam reads the ?limit= parameter: def if there is none, at most max; answers 400 itself if it is not a positive integer.
func limitParam(w http.ResponseWriter, r *http.Request, def, max int) (int, bool) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return def, true
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 {
		httputil.Error(w, http.StatusBadRequest, "limit must be a positive integer")
		return 0, false
	}
	return min(v, max), true
}

// DefinitionsOf serves GET /eval-definitions: {"evals": [{"name": ..., "cases": [{"name", "instruction", "input", "output"}, ...]},
// ...]}, the evals the server can run (the ones in the evals directory), by name. The "name" of an eval is what the "eval" of
// POST .../evals takes.
func (h *Handlers) DefinitionsOf() http.HandlerFunc {
	type evalJSON struct {
		Name        string              `json:"name"`
		Description string              `json:"description,omitempty"`
		Cases       []core.TrainingCase `json:"cases"`
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		evals, err := training.LoadEvals(h.m.EvalDir())
		if err != nil {
			log.WithError(err).Error("training evals: load the eval definitions failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to load the evals")
			return
		}
		out := make([]evalJSON, len(evals))
		for i, e := range evals {
			out[i] = evalJSON{Name: e.Name, Description: e.Description, Cases: e.Cases}
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"evals": out})
	}
}

// StartOf serves POST /models/{id}/evals with an optional JSON body {"eval": "", "cases": [], "limit": 0, "batch_size": 10,
// "judge_model": "", "max_tokens": 1024, "temperature": 0.1} (training.Options): it starts an eval of trained model {id} and answers
// 202 with the eval (training.RunView, status "running"); the work goes on in the background (see GetOf, ListOf, CancelOf).
//   - eval: the name of the eval to run (see DefinitionsOf); default: the eval named like the model's training dataset
//   - cases: run only the cases with these names (default: all of the eval's)
//   - limit: ask only the first n cases (default: all of them)
//   - batch_size: question/answer pairs per judge call (default 10, at most 25)
//   - judge_model: the LLM model that judges (default: the server's default model)
//   - max_tokens, temperature: of the model's answers (default 1024 and 0.1)
//   - experiment: make the eval a run of the experiment of that name (created if needed, see ExperimentsOf);
//     experiment_description and experiment_config (an object) are stored when it is created
//
// 404 for an unknown model, 400 for a bad body, an unknown eval or case, or no eval to default to (the model has no dataset, or no
// eval has its name), 409 when an eval of this model is running already, 503 without a sidecar or an LLM.
func (h *Handlers) StartOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in training.Options
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxStartBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		run, err := h.m.Start(r.Context(), id, in)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusAccepted, run)
	}
}

// ListOf serves GET /models/{id}/evals[?limit=<n>]: {"evals": [...]}, the evals of trained model {id} (training.RunView, without their
// cases), newest first, at most limit (default 50, at most 500). 404 for an unknown model.
func (h *Handlers) ListOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		limit, ok := limitParam(w, r, defaultLimit, maxLimit)
		if !ok {
			return
		}
		list, err := h.m.ListRuns(r.Context(), id, limit)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"evals": list})
	}
}

// GetOf serves GET /evals/{id}: {"run": ..., "cases": [...]}, one eval with the cases stored so far (training.CaseView: question, the
// expected answer, the model's, score and reason), in the order of the eval. 404 for an unknown eval.
func (h *Handlers) GetOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, err := h.m.GetRun(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		cases, err := h.m.RunCases(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"run": run, "cases": cases})
	}
}

// CancelOf serves POST /evals/{id}/cancel: stops a running eval (it ends as "cancelled", keeping the batches done so far) and
// answers 202 with it. 404 for an unknown eval, 409 when it is not running.
func (h *Handlers) CancelOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, err := h.m.CancelRun(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusAccepted, run)
	}
}

// ExperimentsOf serves GET /experiments: {"experiments": [...]}, the training experiments (training.ExperimentView, with their
// number of runs), newest first. An experiment is made by starting an eval with an "experiment" name (see StartOf).
func (h *Handlers) ExperimentsOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := h.m.ListExperiments(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"experiments": list})
	}
}

// ExperimentOf serves GET /experiments/{id}: {"experiment": ..., "runs": [...]}, one experiment with its runs, oldest first. A run
// (training.ExperimentRunView) carries its eval as it is now (so a running one shows its progress), the trained model that was
// evaluated, and the mean score and the share of correct answers. 404 for an unknown experiment.
func (h *Handlers) ExperimentOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		x, runs, err := h.m.GetExperiment(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"experiment": x, "runs": runs})
	}
}

// EventsOf serves GET /evals/{id}/events: the live feed of an eval as Server-Sent Events. Every event's data is a training.Event
// JSON. The first is a "snapshot" (the run and all of its cases so far: the judged and errored ones from the database, and the ones
// the model answered that the judge has not scored yet, with status "answered"); then, while the eval goes on, an "answer" for every
// question the model answers, a "batch" when a batch is judged, and finally an "end" after which the stream closes. For an eval that
// is over, the stream is a snapshot and the "end". A client that reconnects gets a new snapshot, so it applies cases by idx. 404 for
// an unknown eval.
func (h *Handlers) EventsOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		watch, err := h.m.Watch(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer watch.Close()
		serveEvents(r.Context(), w, watch, h.m.Done())
	}
}

func serveEvents(ctx context.Context, w http.ResponseWriter, watch *training.Watch, done <-chan struct{}) {
	rc := http.NewResponseController(w)
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(e training.Event) bool {
		b, err := json.Marshal(e)
		if err != nil {
			return true
		}
		_, err = w.Write([]byte("event: " + e.Type + "\ndata: " + string(b) + "\n\n"))
		return err == nil && rc.Flush() == nil
	}
	if _, err := w.Write([]byte(": connected\n\n")); err != nil || rc.Flush() != nil {
		return
	}
	if !send(training.Event{Type: training.EventSnapshot, Run: watch.Run, Cases: watch.Cases}) {
		return
	}
	ch := watch.Events
	if ch == nil { // nothing is running this eval (any more): it is as stored
		send(training.Event{Type: training.EventEnd, Run: watch.Run})
		return
	}
	keepalive := time.NewTicker(keepaliveTime)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case e, ok := <-ch:
			if !ok { // the eval ended (the "end" event was sent before) or this watcher fell behind: it reconnects
				return
			}
			if !send(e) || e.Type == training.EventEnd {
				return
			}
		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}
