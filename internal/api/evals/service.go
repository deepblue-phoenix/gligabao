// Package evals is the HTTP API of the evals: the routes of a user under /v1/gb/evals (their evals, the runs of those, the case runs
// with their graph, temporal changes, debug view, commentary, rerun and trajectories), and the handlers the admin API
// (/v1/gb/admin/api/evals) wraps to show the same over all users. Every handler acts as an owner (a user id) that a function of type
// Owner returns: a user is always the one of the request, the admin API looks up the owner of the object the request names. An unknown
// id, or the id of an object of another user, is 404.
//
// The run manager (package runmanager) cannot be imported here: it imports package knowledge, which imports package api, which
// mounts these routes. The handlers use the interface Manager, which the manager implements; the root package plugs it in.
package evals

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/atif/atifstore"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/usereval"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// Manager is what the handlers need of the run manager: starting, cancelling and rerunning, the cleanup of eval datasets, the budget
// check of a commentary and the environment of the functions of package usereval.
type Manager interface {
	// Start starts a run of an eval of the owner (see runmanager.Manager.Start for its errors).
	Start(ctx context.Context,
		owner,
		evalName string,
		opts core.RunOptions) (core.EvalRunID, error)
	// Cancel asks a run of the owner to stop (evalsdb.ErrNotFound, evalsdb.ErrRunFinished).
	Cancel(ctx context.Context,
		owner string,
		runID core.EvalRunID) error
	// Rerun starts the rerun of a case run of the owner (see runmanager.Manager.Rerun for its errors).
	Rerun(ctx context.Context,
		owner string,
		src core.CaseRunID,
		opts core.RerunOptions) (core.CaseRunID, error)
	// CanRerun says whether reruns are possible (the server has an LLM); Models are the models a rerun can use.
	CanRerun() bool
	Models() []llm.ModelInfo
	// DeleteDatasets deletes the eval datasets (and their vectors) of a run of the owner.
	DeleteDatasets(ctx context.Context,
		owner string,
		runID core.EvalRunID) error
	// CheckBudget returns core.ErrDailyCap when the owner has spent the cost cap of the day.
	CheckBudget(ctx context.Context,
		owner string) error
	// Env is the environment of the functions of package usereval (the eval files of the users and their limits).
	Env() usereval.Env
}

// Service is what the handlers work with.
type Service struct {
	// DB is the main database; Store reads and writes the evals tables in it.
	DB    *sql.DB
	Store evalsdb.Store
	Mgr   Manager
	// LLM writes the commentaries (nil: none can be written).
	LLM memory_core.LLM
	// TrajectoryDir is the directory of the ATIF files of the file store ("": none), Trajectories the store the server writes them
	// with (nil: atifstore.Default).
	TrajectoryDir string
	Trajectories  atifstore.Store
}

// Kind is the kind of object a request names, for an Owner function that has to look its owner up.
type Kind string

// The kinds of object the handlers ask the owner of.
const (
	KindEvalDef    Kind = "eval_def"
	KindEvalRun    Kind = "eval_run"
	KindCaseRun    Kind = "case_run"
	KindTrajectory Kind = "trajectory"
)

// Owner returns the owner (user id) a request acts as for an object of the given kind, whose id (or name) is key. A user's owner is
// the user of the request whatever the object; the admin API looks up the owner of the object. evalsdb.ErrNotFound: there is no such
// object (404).
type Owner func(r *http.Request,
	kind Kind,
	key string) (string, error)

// UserOwner is the Owner of the user routes: the user of the request, which must not be the reserved owner of the training evals.
func UserOwner(r *http.Request,
	_ Kind,
	_ string) (string, error) {
	user := httputil.UserID(r)
	if user == "" || user == core.SystemUser {
		return "", usereval.ErrInvalidUser
	}
	return user, nil
}

// fail answers the error of an operation: what is the thing that was not found or failed ("run", "case run", ...).
func fail(w http.ResponseWriter,
	err error,
	what string) {
	var invalid *usereval.InvalidError
	switch {
	case errors.Is(err, evalsdb.ErrNotFound), errors.Is(err, usereval.ErrNotFound):
		httputil.Error(w,
			http.StatusNotFound,
			what+" not found")
	case errors.Is(err, usereval.ErrInvalidUser), errors.Is(err, evalsdb.ErrInvalidOwner):
		httputil.Error(w,
			http.StatusBadRequest,
			"invalid user")
	case errors.As(err, &invalid):
		problems := make([]string, len(invalid.Problems))
		for i, p := range invalid.Problems {
			problems[i] = string(p)
		}
		httputil.WriteJSON(w,
			http.StatusBadRequest,
			map[string]any{
				"error":    "invalid eval",
				"problems": problems,
			})
	case errors.Is(err, usereval.ErrTooLarge):
		httputil.Error(w,
			http.StatusRequestEntityTooLarge,
			"the request body is too large")
	case errors.Is(err, usereval.ErrStorage):
		log.WithError(err).Error("evals: storage unavailable")
		httputil.Error(w,
			http.StatusServiceUnavailable,
			"eval storage unavailable")
	case errors.Is(err, core.ErrTooManyRuns), errors.Is(err, core.ErrDailyCap):
		httputil.Error(w,
			http.StatusTooManyRequests,
			err.Error())
	case errors.Is(err, core.ErrNoLLM):
		httputil.Error(w,
			http.StatusServiceUnavailable,
			err.Error())
	case errors.Is(err, core.ErrBadOptions):
		httputil.Error(w,
			http.StatusBadRequest,
			err.Error())
	case errors.Is(err, evalsdb.ErrRunFinished):
		httputil.Error(w,
			http.StatusConflict,
			"the run has already finished")
	default:
		log.WithError(err).Errorf("evals: %s failed", what)
		httputil.Error(w,
			http.StatusInternalServerError,
			"failed to handle "+what)
	}
}

// PathID parses the {id} path value.
func PathID(w http.ResponseWriter,
	r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"),
		10,
		64)
	if err != nil || id <= 0 {
		httputil.Error(w,
			http.StatusBadRequest,
			"invalid id")
		return 0, false
	}
	return id, true
}

// ownerOf returns the owner of the object of the request, or answers (404 or 400) and returns false.
func ownerOf(w http.ResponseWriter,
	r *http.Request,
	own Owner,
	kind Kind,
	key string,
	what string) (string, bool) {
	owner, err := own(r,
		kind,
		key)
	if err != nil {
		fail(w,
			err,
			what)
		return "", false
	}
	return owner, true
}

// active says whether a run is going (pending or running).
func active(r *core.EvalRun) bool { return !r.Status.Finished() }
