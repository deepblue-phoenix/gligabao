package admin

// Remember runs in the admin UI (see docs/remember.md#run-log): the run log of the remember pipeline (gb_remember_runs),
// how many runs ended in each status, and the list of the runs (newest first, filterable) with the state of each stage.
// The outputs of the stages of one run (for debugging) are a separate endpoint, RememberRunOutputsOf, and so is what the run did to
// the temporal state of the facts, RememberRunTemporalOf.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
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

type rememberRunOutputsResponse struct {
	RunID int64 `json:"run_id"`
	// Stage is the stage asked for; "" for all of them.
	Stage string `json:"stage"`
	// Outputs are what the stages of the run produced, in the order they were made (see relational.RememberStageOutput).
	Outputs []relational.RememberStageOutput `json:"outputs"`
}

// RememberRunOutputsOf serves GET /admin/api/remember-run-outputs?run_id=<id>[&stage=<name>][&db=<name>] for one of dbs (the
// first without db): the stage outputs of one remember run (gb_remember_run_stage_outputs), for debugging, those of one stage
// or of all of them. They are not in the list of runs, which would make it heavy: the UI asks for the outputs of a stage when
// the user opens them. An unknown run is 404; a run that has none (the
// outputs were cleared, say) has an empty list.
func RememberRunOutputsOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pick(dbs, q.Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		id, err := strconv.ParseInt(q.Get("run_id"), 10, 64)
		if err != nil || id < 1 {
			httputil.Error(w, http.StatusBadRequest, "invalid run_id")
			return
		}
		stage := q.Get("stage")
		if _, ok := relational.RememberStageDefOf(stage); stage != "" && !ok {
			httputil.Error(w, http.StatusBadRequest, "invalid stage")
			return
		}
		ctx := r.Context()
		if _, err := relational.GetRememberRun(ctx, n.DB, id); err != nil {
			if errors.Is(err, relational.ErrNotFound) {
				httputil.Error(w, http.StatusNotFound, "unknown remember run")
				return
			}
			log.WithError(err).Error("admin: remember run failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the remember run")
			return
		}
		outputs, err := relational.ListRememberStageOutputs(ctx, n.DB, id, stage)
		if err != nil {
			log.WithError(err).Error("admin: remember run outputs failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the stage outputs")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, rememberRunOutputsResponse{RunID: id, Stage: stage, Outputs: outputs})
	}
}

// rememberRunTemporalArtifact is an artifact the temporal events of a run belong to: the run's own, and those of its chunks and of
// the web searches and series observations made for it. ValidAt is its event time (the reference time of the text).
type rememberRunTemporalArtifact struct {
	ID        int64     `json:"id"`
	ParentID  int64     `json:"parent_id,omitempty"`
	Kind      string    `json:"kind"`
	ValidAt   time.Time `json:"valid_at"`
	CreatedAt time.Time `json:"created_at"`
}

type rememberRunTemporalResponse struct {
	RunID int64 `json:"run_id"`
	// ArtifactID is the artifact the run stored; 0 if it did not get as far as storing one.
	ArtifactID int64 `json:"artifact_id"`
	// Artifacts are the artifacts that have events, by ID.
	Artifacts []rememberRunTemporalArtifact `json:"artifacts"`
	// Events are what the run did to the facts (see graph.EdgeEvent, gb_edge_events), oldest first, each with its fact in its
	// current state. Empty for a run that changed no fact, or that ran before the log existed.
	Events []graph.EdgeEvent `json:"events"`
}

// RememberRunTemporalOf serves GET /admin/api/remember-run-temporal?run_id=<id>[&db=<name>] for one of dbs (the first without db):
// what one remember run did to the temporal state of the facts (gb_edge_events: facts created, confirmed, ended, invalidated or
// superseded), for the chunks of a long text and the enrichment artifacts too, plus the artifacts the events belong to (their
// reference time). An unknown run is 404.
func RememberRunTemporalOf(dbs []Named) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pick(dbs, q.Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		id, err := strconv.ParseInt(q.Get("run_id"), 10, 64)
		if err != nil || id < 1 {
			httputil.Error(w, http.StatusBadRequest, "invalid run_id")
			return
		}
		ctx := r.Context()
		run, err := relational.GetRememberRun(ctx, n.DB, id)
		if err != nil {
			if errors.Is(err, relational.ErrNotFound) {
				httputil.Error(w, http.StatusNotFound, "unknown remember run")
				return
			}
			log.WithError(err).Error("admin: remember run failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the remember run")
			return
		}
		out := rememberRunTemporalResponse{RunID: id, Artifacts: []rememberRunTemporalArtifact{}, Events: []graph.EdgeEvent{}}
		if run.ArtifactID != nil {
			out.ArtifactID = *run.ArtifactID
			if out.Events, err = graph.ListEdgeEvents(ctx, n.DB, out.ArtifactID); err != nil {
				log.WithError(err).Error("admin: remember run temporal events failed")
				httputil.Error(w, http.StatusInternalServerError, "failed to read the temporal events")
				return
			}
		}
		if out.Artifacts, err = temporalArtifacts(ctx, n.DB, out.Events); err != nil {
			log.WithError(err).Error("admin: remember run temporal artifacts failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the temporal events")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// temporalArtifacts returns the artifacts the events belong to (in the dataset of the events), by ID; empty for no events.
func temporalArtifacts(ctx context.Context, d *sql.DB, events []graph.EdgeEvent) ([]rememberRunTemporalArtifact, error) {
	out := []rememberRunTemporalArtifact{}
	if len(events) == 0 {
		return out, nil
	}
	var ids []int64
	seen := map[int64]bool{}
	for _, e := range events {
		if e.ArtifactID != 0 && !seen[e.ArtifactID] {
			seen[e.ArtifactID] = true
			ids = append(ids, e.ArtifactID)
		}
	}
	arts, err := graph.ArtifactsByID(ctx, d, events[0].DatasetID, ids)
	if err != nil {
		return nil, err
	}
	for _, a := range arts {
		out = append(out, rememberRunTemporalArtifact{ID: a.ID, ParentID: a.ParentID, Kind: a.Kind, ValidAt: a.ValidAt, CreatedAt: a.CreatedAt})
	}
	return out, nil
}
