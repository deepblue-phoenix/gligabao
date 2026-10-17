package admin

// Remember runs in the admin UI (see docs/remember.md#run-log): the run log of the remember pipeline (gb_remember_runs),
// how many runs ended in each status, and the list of the runs (newest first, filterable) with the state of each stage.

import (
	"net/http"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	log "github.com/sirupsen/logrus"
)

const (
	rememberRunsDefaultLimit = 100
	rememberRunsMaxLimit     = 500
)

// RememberRunCounts is how many remember runs there are, in all and for each status of a whole call. It is never filtered.
type RememberRunCounts struct {
	Total     int `json:"total"`
	Started   int `json:"started"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

type rememberRunsResponse struct {
	Counts RememberRunCounts `json:"counts"`
	// Stages are the stages of the pipeline, in order (the keys of each run's stages).
	Stages []string `json:"stages"`
	// Runs are the runs that match the filter, newest first (Limit of them, after Offset); Total is how many match.
	Runs   []relational.RememberRun `json:"runs"`
	Total  int                      `json:"total"`
	Limit  int                      `json:"limit"`
	Offset int                      `json:"offset"`
}

// RememberRunsOf serves GET /admin/api/remember-runs[?db=<name>][&status=..][&dataset=..][&user_id=..][&limit=..][&offset=..]
// for one of dbs (the first without db): the counts of all the runs by status (never filtered) and the runs that match
// the filters.
func RememberRunsOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pick(dbs, q.Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		limit, offset := rememberRunsDefaultLimit, 0
		if v := q.Get("limit"); v != "" {
			l, err := strconv.Atoi(v)
			if err != nil || l < 1 {
				httputil.Error(w, http.StatusBadRequest, "invalid limit")
				return
			}
			limit = min(l, rememberRunsMaxLimit)
		}
		if v := q.Get("offset"); v != "" {
			o, err := strconv.Atoi(v)
			if err != nil || o < 0 {
				httputil.Error(w, http.StatusBadRequest, "invalid offset")
				return
			}
			offset = o
		}
		status := q.Get("status")
		if status != "" && status != relational.RememberStarted && status != relational.RememberSucceeded && status != relational.RememberFailed {
			httputil.Error(w, http.StatusBadRequest, "invalid status")
			return
		}
		ctx := r.Context()
		fail := func(err error) {
			log.WithError(err).Error("admin: remember runs failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read remember runs")
		}
		out := rememberRunsResponse{Stages: make([]string, 0, len(relational.RememberStages))}
		for _, s := range relational.RememberStages {
			out.Stages = append(out.Stages, s.Name)
		}
		for _, c := range []struct {
			status string
			into   *int
		}{{"", &out.Counts.Total}, {relational.RememberStarted, &out.Counts.Started},
			{relational.RememberSucceeded, &out.Counts.Succeeded}, {relational.RememberFailed, &out.Counts.Failed}} {
			_, total, err := relational.ListRememberRuns(ctx, n.DB, relational.RememberRunFilter{Status: c.status}, 1, 0)
			if err != nil {
				fail(err)
				return
			}
			*c.into = total
		}
		f := relational.RememberRunFilter{UserID: q.Get("user_id"), Dataset: q.Get("dataset"), Status: status}
		runs, total, err := relational.ListRememberRuns(ctx, n.DB, f, limit, offset)
		if err != nil {
			fail(err)
			return
		}
		out.Runs, out.Total, out.Limit, out.Offset = runs, total, limit, offset
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}
