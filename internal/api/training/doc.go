// Package training holds the HTTP handlers of the training API (/v1/gb/training/): starting training jobs on the sidecar,
// listing them, following their progress (the Monitor and its SSE feed), the training datasets (the subdirectories of
// the training datasets directory that have a manifest), the trained models and their inferences, and the evals and audits of
// trained models. They are registered by internal/sys/server; the admin UI is one of their clients.
//
// The handlers only read the request and write the response: what they do is in internal/training.
package training

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
)

// writeErr answers with the status that goes with err: the Kind of a core.Error, 500 for any other error.
func writeErr(w http.ResponseWriter, err error) {
	var e *core.Error
	if !errors.As(err, &e) {
		httputil.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	status := http.StatusInternalServerError
	switch e.Kind {
	case core.KindInvalid:
		status = http.StatusBadRequest
	case core.KindNotFound:
		status = http.StatusNotFound
	case core.KindConflict:
		status = http.StatusConflict
	case core.KindUpstream:
		status = http.StatusBadGateway
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

// pickDB returns the database the ?db= parameter names (the first one if there is none), answering 404 itself if there is not one.
func pickDB(w http.ResponseWriter, r *http.Request, dbs []core.Database) (core.Database, bool) {
	n, ok := core.Pick(dbs, r.URL.Query().Get("db"))
	if !ok {
		httputil.Error(w, http.StatusNotFound, "unknown database")
	}
	return n, ok
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
