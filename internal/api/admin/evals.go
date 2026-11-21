package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/api/evals"
	"github.com/deepblue-phoenix/gligabao/internal/atif/atifstore"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	log "github.com/sirupsen/logrus"
)

// The evals views of the admin UI show the evals of every user (package api/evals has the handlers, which the user routes of
// /v1/gb/evals use too): an eval (core.EvalDef) of a user, its runs (core.EvalRun) and, in a run, its case runs, each with its
// trajectories. The handlers here are thin wrappers: they look the owner of the object the request names up (AdminOwner), and the shared
// handler reads it as that owner. The JSON is what the UI (admin_ui/src/evals.ts) reads. The list takes an optional ?user_id= filter,
// and every eval in it has its user_id.

// AdminOwner is the evals.Owner of the admin API: the owner of the eval def, eval run, case run or trajectory the request names, looked
// up in the evals tables (evalsdb.ErrNotFound if there is none).
func AdminOwner(d *sql.DB,
	s evalsdb.Store) evals.Owner {
	return func(r *http.Request,
		kind evals.Kind,
		key string) (string, error) {
		if kind == evals.KindTrajectory {
			return s.AdminTrajectoryOwner(r.Context(),
				d,
				key)
		}
		var adminKind evalsdb.AdminKind
		switch kind {
		case evals.KindEvalDef:
			adminKind = evalsdb.AdminEvalDef
		case evals.KindEvalRun:
			adminKind = evalsdb.AdminEvalRun
		case evals.KindCaseRun:
			adminKind = evalsdb.AdminCaseRun
		}
		id, err := strconv.ParseInt(key,
			10,
			64)
		if err != nil || id <= 0 {
			return "", evalsdb.ErrNotFound
		}
		return s.AdminOwnerOf(r.Context(),
			d,
			adminKind,
			id)
	}
}

// evalRow is an eval in the admin list: its view with the id of the def and its owner.
type evalRow struct {
	ID     int64  `json:"id"`
	UserID string `json:"user_id"`
	evals.EvalView
}

type evalOverview struct {
	Totals evals.Totals `json:"totals"`
	Evals  []evalRow    `json:"evals"`
}

// EvalOverview serves GET /admin/api/evals[?user_id=]: the evals of every user (or of one) with their counts, and the totals.
func EvalOverview(s *evals.Service) http.HandlerFunc {
	return func(w http.ResponseWriter,
		r *http.Request) {
		ctx := r.Context()
		defs, err := s.Store.AdminListEvalDefs(ctx,
			s.DB,
			evalsdb.AdminFilter{UserID: r.URL.Query().Get("user_id")})
		if err != nil {
			evalFail(w,
				err,
				"evals")
			return
		}
		views, totals, err := s.EvalViews(ctx, defs)
		if err != nil {
			evalFail(w,
				err,
				"evals")
			return
		}
		out := evalOverview{
			Totals: totals,
			Evals: make([]evalRow,
				0,
				len(views)),
		}
		for i := range views {
			out.Evals = append(out.Evals, evalRow{
				ID:       int64(defs[i].ID),
				UserID:   defs[i].UserID,
				EvalView: views[i],
			})
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			out)
	}
}

// evalDetail is an eval with its cases and its runs, newest first.
type evalDetail struct {
	ID          int64           `json:"id"`
	UserID      string          `json:"user_id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Cases       []string        `json:"cases"`
	Runs        []evals.RunView `json:"runs"`
}

// defResolver finds the eval def of the {id} path value and its owner.
func defResolver(s *evals.Service) evals.DefResolver {
	own := AdminOwner(s.DB, s.Store)
	return func(w http.ResponseWriter,
		r *http.Request) (string, *core.EvalDef, bool) {
		id, ok := evals.PathID(w, r)
		if !ok {
			return "", nil, false
		}
		owner, err := own(r,
			evals.KindEvalDef,
			r.PathValue("id"))
		if err != nil {
			evalFail(w,
				err,
				"eval")
			return "", nil, false
		}
		def, err := s.Store.GetEvalDef(r.Context(),
			s.DB,
			owner,
			core.EvalDefID(id))
		if err != nil {
			evalFail(w,
				err,
				"eval")
			return "", nil, false
		}
		return owner, def, true
	}
}

// EvalDef serves GET /admin/api/evals/defs/{id}: the eval with the names of its cases and its runs (newest first).
func EvalDef(s *evals.Service) http.HandlerFunc {
	resolve := defResolver(s)
	return func(w http.ResponseWriter,
		r *http.Request) {
		owner, def, ok := resolve(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		cases, err := s.Store.ListCaseDefs(ctx,
			s.DB,
			owner,
			def.ID)
		if err != nil {
			evalFail(w,
				err,
				"eval")
			return
		}
		runs, err := s.Store.ListEvalRunsOfDef(ctx,
			s.DB,
			owner,
			def.ID,
			0)
		if err != nil {
			evalFail(w,
				err,
				"runs")
			return
		}
		out := evalDetail{
			ID:          int64(def.ID),
			UserID:      owner,
			Name:        def.Name,
			Description: def.Description,
			Cases:       make([]string, 0, len(cases)),
			Runs:        make([]evals.RunView, 0, len(runs)),
		}
		for _, c := range cases {
			out.Cases = append(out.Cases, c.Name)
		}
		for i := range runs {
			out.Runs = append(out.Runs, evals.ViewOfRun(&runs[i], def.Name))
		}
		httputil.WriteJSON(w,
			http.StatusOK,
			out)
	}
}

// EvalCaseHistory serves GET /admin/api/evals/defs/{id}/history?case=..: the status of one case in each run of the eval, oldest run first.
func EvalCaseHistory(s *evals.Service) http.HandlerFunc {
	return s.History(defResolver(s))
}

// EvalRun serves GET /admin/api/evals/runs/{id}: the run with its cases and their trajectory references.
func EvalRun(s *evals.Service) http.HandlerFunc {
	return s.GetRun(AdminOwner(s.DB, s.Store))
}

// EvalCaseRunGraph serves GET /admin/api/evals/case_runs/{id}/graph: the knowledge graph the case built, read from the dataset the case
// ran in (same JSON as /admin/api/graph, with the in-context counts).
func EvalCaseRunGraph(s *evals.Service) http.HandlerFunc {
	return s.Graph(AdminOwner(s.DB, s.Store),
		func(ctx context.Context,
			d *sql.DB,
			g *memory.GraphExport) (any, error) {
			return withInContext(ctx,
				d,
				g)
		})
}

// EvalCaseRunTemporal serves GET /admin/api/evals/case_runs/{id}/temporal: what the remember calls of the case did to the temporal
// state of the facts (the same events as the temporal view of a remember run, RememberRunTemporalOf).
func EvalCaseRunTemporal(s *evals.Service) http.HandlerFunc {
	return s.Temporal(AdminOwner(s.DB, s.Store))
}

// EvalCaseRunDebug serves GET /admin/api/evals/case_runs/{id}/debug.
func EvalCaseRunDebug(s *evals.Service) http.HandlerFunc {
	return s.Debug(AdminOwner(s.DB, s.Store))
}

// EvalCaseRunCommentary serves POST /admin/api/evals/case_runs/{id}/commentary.
func EvalCaseRunCommentary(s *evals.Service) http.HandlerFunc {
	return s.Commentary(AdminOwner(s.DB, s.Store))
}

// EvalCaseRunRerun serves POST /admin/api/evals/case_runs/{id}/rerun: the rerun is made as the owner of the case run.
func EvalCaseRunRerun(s *evals.Service) http.HandlerFunc {
	return s.Rerun(AdminOwner(s.DB, s.Store))
}

// EvalTrajectory serves GET /admin/api/evals/trajectories/{id}: the ATIF trajectory JSON of a trajectory a case run recorded.
func EvalTrajectory(s *evals.Service) http.HandlerFunc {
	return s.Trajectory(AdminOwner(s.DB, s.Store))
}

// evalFail answers the error of an admin evals read: 404 for an unknown object, else 500.
func evalFail(w http.ResponseWriter,
	err error,
	what string) {
	if errors.Is(err, evalsdb.ErrNotFound) {
		httputil.Error(w,
			http.StatusNotFound,
			what+" not found")
		return
	}
	log.WithError(err).Errorf("admin: %s failed", what)
	httputil.Error(w,
		http.StatusInternalServerError,
		"failed to load "+what)
}

// pathID parses the {id} path value.
func pathID(w http.ResponseWriter,
	r *http.Request) (int64, bool) {
	return evals.PathID(w, r)
}

// readTrajectoryFile reads the JSON file of a recorded trajectory (of any user): from the trajectory directory dir first (when not
// empty), then from the path stored with the reference (a file, or the s3:// location of a trace in the S3 store).
func readTrajectoryFile(ref *core.KnowledgeTrajectory,
	dir string) ([]byte, error) {
	return evals.ReadTrajectoryFile(context.Background(),
		ref,
		dir,
		"",
		atifstore.Default)
}
