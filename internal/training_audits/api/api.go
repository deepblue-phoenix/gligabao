// Package api is the HTTP side of the training audits (package training_audits): the handlers that start, list, read, cancel and
// watch the audits of trained models. They are made over a *training_audits.Manager, and Register puts them on a mux under a path
// prefix (the server picks it: "/v1/gb/training" gives the paths of the doc comments below).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
)

const (
	maxStartBody  = 1 << 14
	defaultLimit  = 50
	maxLimit      = 500
	keepaliveTime = 15 * time.Second
)

// Handlers are the endpoints of the training audits.
type Handlers struct {
	m *training_audits.Manager
}

// New makes the handlers over m.
func New(m *training_audits.Manager) *Handlers { return &Handlers{m: m} }

// Register puts the handlers on mux, under prefix ("" or e.g. "/v1/gb/training"):
//
//	POST {prefix}/models/{id}/audits        StartOf
//	GET  {prefix}/models/{id}/audits        ListOf
//	GET  {prefix}/audits/{id}               GetOf
//	GET  {prefix}/audits/{id}/events        EventsOf
//	POST {prefix}/audits/{id}/cancel        CancelOf
func (h *Handlers) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("POST "+prefix+"/models/{id}/audits", h.StartOf())
	mux.HandleFunc("GET "+prefix+"/models/{id}/audits", h.ListOf())
	mux.HandleFunc("GET "+prefix+"/audits/{id}", h.GetOf())
	mux.HandleFunc("GET "+prefix+"/audits/{id}/events", h.EventsOf())
	mux.HandleFunc("POST "+prefix+"/audits/{id}/cancel", h.CancelOf())
}

// writeErr answers with the status of the Kind of err (500 for any other error).
func writeErr(w http.ResponseWriter, err error) {
	var e *training_audits.Error
	if !errors.As(err, &e) {
		httputil.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	status := http.StatusInternalServerError
	switch e.Kind {
	case training_audits.KindInvalid:
		status = http.StatusBadRequest
	case training_audits.KindNotFound:
		status = http.StatusNotFound
	case training_audits.KindConflict:
		status = http.StatusConflict
	case training_audits.KindUnavailable:
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

// StartOf serves POST /models/{id}/audits with an optional JSON body {"limit": 0, "batch_size": 10, "judge_model": "",
// "max_tokens": 1024, "temperature": 0.1} (training_audits.Options): it starts an audit of trained model {id} and answers 202 with
// the audit (core.AuditRun, status "running"); the work goes on in the background (see GetOf, ListOf, CancelOf).
//   - limit: ask only the first n examples of the dataset (default: all of them)
//   - batch_size: question/answer pairs per judge call (default 10, at most 25)
//   - judge_model: the LLM model that judges (default: the server's default model)
//   - max_tokens, temperature: of the model's answers (default 1024 and 0.1)
//
// 404 for an unknown model, 400 for a bad body or when the model's dataset cannot be read (none recorded, gone from the training
// datasets directory, invalid), 409 when an audit of this model is running already, 503 without a sidecar or an LLM.
func (h *Handlers) StartOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var in training_audits.Options
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

// ListOf serves GET /models/{id}/audits[?limit=<n>]: {"audits": [...]}, the audits of trained model {id} (core.AuditRun, without
// their case runs), newest first, at most limit (default 50, at most 500). 404 for an unknown model.
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
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"audits": list})
	}
}

// GetOf serves GET /audits/{id}: the audit (core.AuditRun) with its "case_runs" stored so far (core.AuditCaseRun: the question,
// the answer the model was trained to give, the model's, the judge's score and reason), in dataset order. 404 for an unknown audit.
func (h *Handlers) GetOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, err := h.m.GetRunWithCaseRuns(r.Context(), core.AuditRunID(id))
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, run)
	}
}

// CancelOf serves POST /audits/{id}/cancel: stops a running audit (it ends as "cancelled", keeping the batches done so far) and
// answers 202 with it. 404 for an unknown audit, 409 when it is not running.
func (h *Handlers) CancelOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, err := h.m.CancelRun(r.Context(), core.AuditRunID(id))
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusAccepted, run)
	}
}

// EventsOf serves GET /audits/{id}/events: the live feed of an audit as Server-Sent Events. Every event's data is a
// training_audits.Event JSON. The first is a "snapshot" (the run and all of its case runs so far: the ones stored, and the ones
// the model answered that the judge has not scored yet, with status "running"); then, while the audit goes on, an "answer" for
// every question the model answers, a "batch" when a batch ends, and finally an "end" after which the stream closes. For an audit
// that is over, the stream is a snapshot and the "end". A client that reconnects gets a new snapshot, so it applies case runs by
// idx. 404 for an unknown audit.
func (h *Handlers) EventsOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		watch, err := h.m.Watch(r.Context(), core.AuditRunID(id))
		if err != nil {
			writeErr(w, err)
			return
		}
		defer watch.Close()
		serveEvents(r.Context(), w, watch, h.m.Done())
	}
}

func serveEvents(ctx context.Context, w http.ResponseWriter, watch *training_audits.Watch, done <-chan struct{}) {
	rc := http.NewResponseController(w)
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(e training_audits.Event) bool {
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
	if !send(training_audits.Event{Type: training_audits.EventSnapshot, Run: watch.Run, CaseRuns: watch.CaseRuns}) {
		return
	}
	ch := watch.Events
	if ch == nil { // nothing is running this audit (any more): it is as stored
		send(training_audits.Event{Type: training_audits.EventEnd, Run: watch.Run})
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
			if !ok { // the audit ended (the "end" event was sent before) or this watcher fell behind: it reconnects
				return
			}
			if !send(e) || e.Type == training_audits.EventEnd {
				return
			}
		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}
