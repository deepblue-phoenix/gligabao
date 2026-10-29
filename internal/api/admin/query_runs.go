package admin

// Query runs in the admin API (see docs/query.md#run-log): the run log of the query pipeline (gb_query_runs), how many runs ended in
// each status, and the list of the runs (newest first, filterable) with the state of each stage. Like the remember runs
// (remember_runs.go).

import (
	"net/http"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// QueryRunCounts is how many query runs there are, in all and for each status of a whole call. It is never filtered.
type QueryRunCounts = RememberRunCounts

type queryRunsResponse struct {
	Counts QueryRunCounts `json:"counts"`
	// Stages are the stages of the pipeline, in order (the keys of each run's stages).
	Stages []string `json:"stages"`
	// Runs are the runs that match the filter, newest first (Limit of them, after Offset); Total is how many match.
	Runs   []relational.QueryRun `json:"runs"`
	Total  int                   `json:"total"`
	Limit  int                   `json:"limit"`
	Offset int                   `json:"offset"`
}

// QueryRunsOf serves GET /admin/api/query-runs[?db=<name>][&status=..][&dataset=..][&user_id=..][&limit=..][&offset=..] for one of
// dbs (the first without db): the counts of all the runs by status (never filtered) and the runs that match the filters.
func QueryRunsOf(dbs []Named) http.HandlerFunc {
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
			log.WithError(err).Error("admin: query runs failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read query runs")
		}
		out := queryRunsResponse{Stages: make([]string, 0, len(relational.QueryStages))}
		for _, s := range relational.QueryStages {
			out.Stages = append(out.Stages, s.Name)
		}
		for _, c := range []struct {
			status string
			into   *int
		}{{"", &out.Counts.Total}, {relational.RememberStarted, &out.Counts.Started},
			{relational.RememberSucceeded, &out.Counts.Succeeded}, {relational.RememberFailed, &out.Counts.Failed}} {
			_, total, err := relational.ListQueryRuns(ctx, n.DB, relational.QueryRunFilter{Status: c.status}, 1, 0)
			if err != nil {
				fail(err)
				return
			}
			*c.into = total
		}
		f := relational.QueryRunFilter{UserID: q.Get("user_id"), Dataset: q.Get("dataset"), Status: status}
		runs, total, err := relational.ListQueryRuns(ctx, n.DB, f, limit, offset)
		if err != nil {
			fail(err)
			return
		}
		out.Runs, out.Total, out.Limit, out.Offset = runs, total, limit, offset
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}
