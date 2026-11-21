package evals

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
)

// DefResolver finds the eval a request names and its owner, or answers (404 or 400) and returns false.
type DefResolver func(w http.ResponseWriter,
	r *http.Request) (owner string, def *core.EvalDef, ok bool)

// DefByName is the DefResolver of the user routes: the eval called {name} of the owner.
func (s *Service) DefByName(own Owner) DefResolver {
	return func(w http.ResponseWriter,
		r *http.Request) (string, *core.EvalDef, bool) {
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "eval")
		if !ok {
			return "", nil, false
		}
		def, err := s.Store.GetEvalDefByName(r.Context(),
			s.DB,
			owner,
			r.PathValue("name"))
		if err != nil {
			fail(w,
				err,
				"eval")
			return "", nil, false
		}
		return owner, def, true
	}
}

// runRequest is the body of POST /v1/gb/evals/{name}/runs.
type runRequest struct {
	Cases     []string          `json:"cases,omitempty"`
	Models    map[string]string `json:"models,omitempty"`
	Reasoning map[string]string `json:"reasoning,omitempty"`
	Geo       *bool             `json:"geo,omitempty"`
}

// readJSON decodes the optional JSON body of a request (an empty body leaves v as it is); it answers 400 and returns false for a
// body that is too large or not valid. Unknown fields are refused, so that a typo in an option is not silently ignored.
func readJSON(w http.ResponseWriter,
	r *http.Request,
	v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w,
		r.Body,
		1<<16))
	if err != nil {
		httputil.Error(w,
			http.StatusBadRequest,
			"request body too large or unreadable")
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httputil.Error(w,
			http.StatusBadRequest,
			"request body is not valid JSON: "+err.Error())
		return false
	}
	return true
}

// StartRun serves POST /v1/gb/evals/{name}/runs: it starts a run of the eval and answers 202 with the run, pending; it goes on in the
// background. 400 for options that are not valid or an eval that is not valid, 404 for an unknown eval, 429 over the limits of runs or
// of the cost, 503 without an LLM or when the files cannot be read.
func (s *Service) StartRun(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		owner, ok := ownerOf(w, r, own, KindEvalDef, "", "eval")
		if !ok {
			return
		}
		var req runRequest
		if !readJSON(w, r, &req) {
			return
		}
		id, err := s.Mgr.Start(r.Context(),
			owner,
			r.PathValue("name"),
			core.RunOptions{
				Cases:     req.Cases,
				Models:    req.Models,
				Reasoning: req.Reasoning,
				Geo:       req.Geo,
			})
		if err != nil {
			fail(w,
				err,
				"eval")
			return
		}
		httputil.WriteJSON(w,
			http.StatusAccepted,
			map[string]any{
				"run_id": int64(id),
				"status": core.StatusPending,
			})
	}
}

// ListRuns serves GET /v1/gb/evals/{name}/runs?limit=: the runs of the eval, newest first (default 50, at most 200).
func (s *Service) ListRuns(resolve DefResolver) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		owner, def, ok := resolve(w, r)
		if !ok {
			return
		}
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				httputil.Error(w,
					http.StatusBadRequest,
					"invalid limit")
				return
			}
			limit = min(n, 200)
		}
		runs, err := s.Store.ListEvalRunsOfDef(r.Context(),
			s.DB,
			owner,
			def.ID,
			limit)
		if err != nil {
			fail(w,
				err,
				"runs")
			return
		}
		views := make([]RunView,
			0,
			len(runs))
		for i := range runs {
			views = append(views, ViewOfRun(&runs[i], def.Name))
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			map[string]any{"runs": views})
	}
}

// CaseOf is a case run of a knowledge eval with the names of its eval and its case.
type CaseOf struct {
	Eval, Name string
	Run        core.CaseRun
	// EvalRunParams are the params of the eval run that holds the case run (they say whether it is a rerun).
	EvalRunParams core.Params
}

// CaseRuns returns the case runs of the knowledge eval run er of the owner, with their metadata, in the order of the cases.
func (s *Service) CaseRuns(ctx context.Context,
	owner string,
	er *core.EvalRun) ([]CaseOf, error) {
	if er.Type != core.TypeKnowledge {
		return nil, nil
	}
	def, err := s.Store.GetEvalDef(ctx,
		s.DB,
		owner,
		er.DefID)
	if err != nil {
		return nil, err
	}
	cases, err := s.Store.ListCaseDefs(ctx,
		s.DB,
		owner,
		er.DefID)
	if err != nil {
		return nil, err
	}
	names := make(map[core.CaseDefID]string, len(cases))
	for _, c := range cases {
		names[c.ID] = c.Name
	}
	runs, err := s.Store.ListCaseRuns(ctx,
		s.DB,
		owner,
		er.ID)
	if err != nil {
		return nil, err
	}
	out := make([]CaseOf,
		0,
		len(runs))
	for _, c := range runs {
		out = append(out, CaseOf{
			Eval:          def.Name,
			Name:          names[c.DefID],
			Run:           c,
			EvalRunParams: er.Params,
		})
	}
	return out, nil
}

// GetRun serves GET /v1/gb/evals/runs/{id}: the run with its case runs and their trajectory references. The artifacts of each
// trajectory are read from the dataset its case ran in.
func (s *Service) GetRun(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		id, ok := PathID(w, r)
		if !ok {
			return
		}
		owner, ok := ownerOf(w, r, own, KindEvalRun, r.PathValue("id"), "run")
		if !ok {
			return
		}
		ctx := r.Context()
		er, err := s.Store.GetEvalRun(ctx,
			s.DB,
			owner,
			core.EvalRunID(id))
		if err != nil {
			fail(w,
				err,
				"run")
			return
		}
		def, err := s.Store.GetEvalDef(ctx,
			s.DB,
			owner,
			er.DefID)
		if err != nil {
			fail(w,
				err,
				"run")
			return
		}
		cases, err := s.CaseRuns(ctx,
			owner,
			er)
		if err != nil {
			fail(w,
				err,
				"run")
			return
		}
		out := ViewOfRun(er,
			def.Name)
		out.Cases = make([]CaseRunView,
			0,
			len(cases))
		for i := range cases {
			v, err := ViewWithTrajectories(ctx,
				s.DB,
				&cases[i])
			if err != nil {
				fail(w,
					err,
					"run")
				return
			}
			out.Cases = append(out.Cases, v)
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			out)
	}
}

// ViewWithTrajectories is the view of a case run with its trajectories and their artifacts.
func ViewWithTrajectories(ctx context.Context,
	d *sql.DB,
	c *CaseOf) (CaseRunView, error) {
	v := ViewOfCase(&c.Run,
		c.Eval,
		c.Name)
	v.Rerun = RerunOf(c.EvalRunParams)
	m := KnowledgeMeta(&c.Run)
	var all map[int64]graph.Artifact // the dataset's artifacts, loaded when a trajectory needs them
	for _, t := range m.Trajectories {
		tv := TrajectoryView{
			Seq:          t.Seq + 1,
			Kind:         t.Kind,
			TrajectoryID: t.TrajectoryID,
			AgentName:    t.AgentName,
			Steps:        t.Steps,
			CostUSD:      t.CostUSD,
			ArtifactIDs:  OrEmpty(t.ArtifactIDs),
			Artifacts:    []ArtifactView{},
			CreatedAt:    t.CreatedAt,
		}
		if len(t.ArtifactIDs) > 0 && m.DatasetID != nil {
			if all == nil {
				list, err := graph.ListArtifacts(ctx,
					d,
					*m.DatasetID)
				if err != nil {
					return v, err
				}
				all = make(map[int64]graph.Artifact, len(list))
				for _, a := range list {
					all[a.ID] = a
				}
			}
			for _, id := range t.ArtifactIDs {
				if a, ok := all[id]; ok {
					tv.Artifacts = append(tv.Artifacts, ArtifactView{
						ID:        a.ID,
						Kind:      a.Kind,
						Text:      a.Text,
						ValidAt:   a.ValidAt,
						CreatedAt: a.CreatedAt,
					})
				}
			}
		}
		v.Trajectories = append(v.Trajectories, tv)
	}
	return v, nil
}

// CancelRun serves POST /v1/gb/evals/runs/{id}/cancel: it asks the run to stop. 202 with the run; 404; 409 if it has finished.
func (s *Service) CancelRun(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		id, ok := PathID(w, r)
		if !ok {
			return
		}
		owner, ok := ownerOf(w, r, own, KindEvalRun, r.PathValue("id"), "run")
		if !ok {
			return
		}
		ctx := r.Context()
		if err := s.Mgr.Cancel(ctx,
			owner,
			core.EvalRunID(id)); err != nil {
			fail(w,
				err,
				"run")
			return
		}
		status := core.StatusRunning
		if er, err := s.Store.GetEvalRun(ctx,
			s.DB,
			owner,
			core.EvalRunID(id)); err == nil {
			status = er.Status
		}
		httputil.WriteJSON(w,
			http.StatusAccepted,
			map[string]any{
				"run_id": id,
				"status": status,
			})
	}
}

// DeleteRun serves DELETE /v1/gb/evals/runs/{id}: the run, its case runs and its eval datasets. 204; 404; 409 while it is going.
func (s *Service) DeleteRun(own Owner) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		id, ok := PathID(w, r)
		if !ok {
			return
		}
		owner, ok := ownerOf(w, r, own, KindEvalRun, r.PathValue("id"), "run")
		if !ok {
			return
		}
		ctx := r.Context()
		er, err := s.Store.GetEvalRun(ctx,
			s.DB,
			owner,
			core.EvalRunID(id))
		if err != nil {
			fail(w,
				err,
				"run")
			return
		}
		if active(er) {
			httputil.Error(w,
				http.StatusConflict,
				"the run is in progress")
			return
		}
		if err := s.Mgr.DeleteDatasets(ctx,
			owner,
			er.ID); err != nil {
			fail(w,
				err,
				"run")
			return
		}
		if err := s.Store.DeleteEvalRun(ctx,
			s.DB,
			owner,
			er.ID); err != nil {
			fail(w,
				err,
				"run")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// History serves GET /v1/gb/evals/{name}/history?case=NAME: the result of one case in each run of the eval, oldest run first.
func (s *Service) History(resolve DefResolver) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		owner, def, ok := resolve(w, r)
		if !ok {
			return
		}
		name := r.URL.Query().Get("case")
		if name == "" {
			httputil.Error(w,
				http.StatusBadRequest,
				"case is required")
			return
		}
		ctx := r.Context()
		results, err := s.Store.ListCaseHistoryOfEval(ctx,
			s.DB,
			owner,
			def.ID,
			name)
		if err != nil {
			fail(w,
				err,
				"history")
			return
		}
		runs, err := s.Store.ListEvalRunsOfDef(ctx,
			s.DB,
			owner,
			def.ID,
			0)
		if err != nil {
			fail(w,
				err,
				"history")
			return
		}
		cases := make([]core.CaseRun,
			0,
			len(results))
		for _, e := range results {
			cases = append(cases, e.CaseRun)
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			CaseHistory{
				Eval:   def.Name,
				Name:   name,
				Points: HistoryPoints(cases, runs),
			})
	}
}

// ByName dispatches the routes of the form /v1/gb/evals/{name}/{sub} (GET and POST) to the handler of sub. Go's mux cannot hold
// /evals/{name}/runs next to /evals/runs/{id} (neither pattern is the more specific one), so the routes with a name and one more
// segment are one pattern that picks by the last segment; the reserved names (runs, case_runs, trajectories) keep the others apart.
func ByName(routes map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		h, ok := routes[r.PathValue("sub")]
		if !ok {
			httputil.Error(w,
				http.StatusNotFound,
				"not found")
			return
		}
		h(w, r)
	}
}
