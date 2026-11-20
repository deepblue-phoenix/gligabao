package remember

// The remember runs of a user, over REST (the run log of docs/remember/remember.md#run-log, scoped to the user of the request):
//
//	GET /v1/gb/remember/runs[?limit=..][&offset=..][&dataset=..]   the user's runs, newest first
//	GET /v1/gb/remember/runs/{id}                                  one run of the user
//
// A run is the row of gb_remember_runs (its params are the flags the call was made with) plus cost_usd, the sum of the cost of
// the inferences of all its stages. A run of another user is a 404. The user_id is removed from the params it returns (see
// withoutUserID), and the errors of a failed run and its stages are replaced by a generic message (see withoutInternalErrors).

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"net/http"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

const (
	runsDefaultLimit = 50
	runsMaxLimit     = 200
)

// runView is a run with its cost.
type runView struct {
	relational.RememberRun
	CostUSD float64 `json:"cost_usd"`
}

type runsResponse struct {
	Runs   []runView `json:"runs"`
	Total  int       `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
}

// runCost is the sum of the cost of the inferences of every stage of the run. An inference that cannot be read counts as 0.
func runCost(ctx context.Context,
	d *sql.DB,
	run relational.RememberRun) float64 {
	total := 0.0
	for _, stage := range run.Stages {
		for _, id := range stage.InferenceIDs {
			inf, err := relational.GetInference(ctx,
				d,
				id)
			if err == nil && inf != nil {
				total += inf.CostUSD
			}
		}
	}
	return total
}

// userFacingError is what the endpoints say of a failed run or stage: the generic message of the 502 of POST /v1/gb/remember.
// The stored error (the cause, with provider, model, SQL or file details) is internal and stays in the run log and the admin API.
const userFacingError = "failed to process text"

// publicError maps a stored error to the user-facing one: nil stays nil (it did not fail), anything else becomes the generic message.
func publicError(stored *string) *string {
	if stored == nil {
		return nil
	}
	msg := userFacingError
	return &msg
}

// withoutInternalErrors returns the run with the errors of the run and of its stages replaced by publicError. The run is not changed.
func withoutInternalErrors(run relational.RememberRun) relational.RememberRun {
	stages := make(map[string]relational.RememberStage, len(run.Stages))
	for name, stage := range run.Stages {
		stage.Error = publicError(stage.Error)
		stages[name] = stage
	}
	run.Stages = stages
	run.Error = publicError(run.Error)
	return run
}

// withoutUserID returns the run without the user_id of its params. REMOVAL OF THE user_id: the params of a run (as stored in
// gb_remember_runs) hold the user_id of the call, but these endpoints only return the runs of the user of the request, so the
// caller knows it already and a UI that shows the flags of a run does not need it. The stored params are not changed.
func withoutUserID(run relational.RememberRun) relational.RememberRun {
	params := maps.Clone(run.Params)
	delete(params, "user_id")
	run.Params = params
	return run
}

func withCost(ctx context.Context,
	d *sql.DB,
	runs []relational.RememberRun) []runView {
	out := make([]runView, 0, len(runs))
	for _, run := range runs {
		out = append(out,
			runView{
				RememberRun: withoutInternalErrors(withoutUserID(run)),
				CostUSD: runCost(ctx,
					d,
					run),
			})
	}
	return out
}

// parseWindow reads limit and offset of the query; ok is false for a malformed one.
func parseWindow(r *http.Request) (limit, offset int, ok bool) {
	limit, offset = runsDefaultLimit, 0
	q := r.URL.Query()
	if v := q.Get("limit"); v != "" {
		l, err := strconv.Atoi(v)
		if err != nil || l < 1 {
			return 0, 0, false
		}
		limit = min(l, runsMaxLimit)
	}
	if v := q.Get("offset"); v != "" {
		o, err := strconv.Atoi(v)
		if err != nil || o < 0 {
			return 0, 0, false
		}
		offset = o
	}
	return limit, offset, true
}

// RunsHandler serves GET /v1/gb/remember/runs: the runs of the user of the request, newest first.
func RunsHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID := httputil.UserID(r)
		limit, offset, ok := parseWindow(r)
		if userID == "" || !ok {
			httputil.Error(w,
				http.StatusBadRequest,
				"invalid user, limit or offset")
			return
		}
		runs, total, err := relational.ListRememberRuns(r.Context(),
			d,
			relational.RememberRunFilter{
				UserID:  userID,
				Dataset: r.URL.Query().Get("dataset"),
			},
			limit,
			offset)
		if err != nil {
			log.WithError(err).Error("list remember runs failed")
			httputil.Error(w,
				http.StatusInternalServerError,
				"failed to read remember runs")
			return
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			runsResponse{
				Runs: withCost(r.Context(),
					d,
					runs),
				Total:  total,
				Limit:  limit,
				Offset: offset,
			})
	}
}

// RunHandler serves GET /v1/gb/remember/runs/{id}: one run of the user of the request.
func RunHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		userID := httputil.UserID(r)
		id, idErr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if userID == "" || idErr != nil || id < 1 {
			httputil.Error(w,
				http.StatusBadRequest,
				"invalid user or run id")
			return
		}
		run, err := relational.GetRememberRun(r.Context(),
			d,
			id)
		switch {
		case errors.Is(err, relational.ErrNotFound), err == nil && run.UserID != userID:
			httputil.Error(w,
				http.StatusNotFound,
				"unknown remember run")
		case err != nil:
			log.WithError(err).Error("get remember run failed")
			httputil.Error(w,
				http.StatusInternalServerError,
				"failed to read the remember run")
		default:
			httputil.WriteJSON(w,
				http.StatusOK,
				withCost(r.Context(),
					d,
					[]relational.RememberRun{*run})[0])
		}
	}
}
