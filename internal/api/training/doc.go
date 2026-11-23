// Package training holds the HTTP handlers of the training API (/v1/gb/training/): starting training jobs on the sidecar,
// listing them, following their progress (the Monitor and its SSE feed), the training datasets (the subdirectories of
// the training datasets directory that have a manifest), the trained models and their inferences, and the evals of
// trained models. They are registered by internal/sys/server; the admin UI is one of their clients.
//
// The handlers only read the request and write the response: what they do is in internal/training. Every route acts as the user of the
// request (see userOf), except those of the datasets (files on disk, shared) and the lists of the registered hooks.
package training

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
)

// writeErr answers with the status that goes with err: the Kind of a core.Error, 500 for any other error.
func writeErr(w http.ResponseWriter,
	err error) {
	var e *core.Error
	if !errors.As(err, &e) {
		httputil.Error(w,
			http.StatusInternalServerError,
			"internal error")
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
	httputil.Error(w,
		status,
		e.Msg)
}

// userOf is the checked ID of the user of the request (the routes are behind the authentication, which sets it); if it is not valid the
// 400 is already written and ok is false. Everything but the training datasets is per user: a job, a model, an eval or an audit of
// another user is as unknown as one that does not exist (404).
func userOf(w http.ResponseWriter,
	r *http.Request) (userID string, ok bool) {
	userID, err := memory_core.ResolveUserID(httputil.UserID(r))
	if err != nil {
		httputil.Error(w,
			http.StatusBadRequest,
			err.Error())
		return "", false
	}
	return userID, true
}

// pathID reads the {id} of the path, answering 400 itself if it is not a positive integer.
func pathID(w http.ResponseWriter,
	r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"),
		10,
		64)
	if err != nil || id < 1 {
		httputil.Error(w,
			http.StatusBadRequest,
			"id must be a positive integer")
		return 0, false
	}
	return id, true
}

// limitParam reads the ?limit= parameter: def if there is none, at most max; answers 400 itself if it is not a positive integer.
func limitParam(w http.ResponseWriter,
	r *http.Request,
	def,
	max int) (int, bool) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return def, true
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 {
		httputil.Error(w,
			http.StatusBadRequest,
			"limit must be a positive integer")
		return 0, false
	}
	return min(v, max), true
}
