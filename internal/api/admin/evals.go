package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/experiments"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	log "github.com/sirupsen/logrus"
)

// EvalTotals are the eval counts over all experiments.
type EvalTotals struct {
	Experiments int `json:"experiments"`
	Runs        int `json:"runs"`
	// Evals is the number of distinct evals (suite + case name) any experiment executed (counted once across experiments).
	Evals int `json:"evals"`
	// Executions is the number of eval results over all finished runs; Passed of them passed, Failed did not.
	Executions int     `json:"executions"`
	Passed     int     `json:"passed"`
	Failed     int     `json:"failed"`
	CostUSD    float64 `json:"cost_usd"`
}

// experimentView is an experiment with its counts, as listed in the admin UI.
type experimentView struct {
	experiments.Experiment
	Runs       int     `json:"runs"`
	Evals      int     `json:"evals"`
	RunsPassed int     `json:"runs_passed"`
	RunsFailed int     `json:"runs_failed"`
	CostUSD    float64 `json:"cost_usd"`
	// LastRun is the newest run (nil if the experiment has none).
	LastRun *experiments.Run `json:"last_run,omitempty"`
}

type evalOverview struct {
	Totals      EvalTotals       `json:"totals"`
	Experiments []experimentView `json:"experiments"`
}

type experimentDetail struct {
	experiments.Experiment
	// Runs are the experiment's runs, newest first.
	Runs []experiments.Run `json:"runs"`
}

// evalHistory is the progression of one eval over the runs of an experiment.
type evalHistory struct {
	Suite  string                  `json:"suite"`
	Name   string                  `json:"name"`
	Points []experiments.EvalPoint `json:"points"`
}

func evalFail(w http.ResponseWriter, err error, what string) {
	if errors.Is(err, experiments.ErrNotFound) {
		httputil.Error(w, http.StatusNotFound, what+" not found")
		return
	}
	log.WithError(err).Errorf("admin: %s failed", what)
	httputil.Error(w, http.StatusInternalServerError, "failed to load "+what)
}

// EvalOverview serves GET /admin/api/evals: every experiment with its counts, and the totals over all of them.
func EvalOverview(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		list, err := experiments.List(ctx, d)
		if err != nil {
			evalFail(w, err, "experiments")
			return
		}
		out := evalOverview{Experiments: make([]experimentView, 0, len(list))}
		out.Totals.Experiments = len(list)
		distinct := map[experiments.EvalRef]bool{}
		for _, e := range list {
			full, err := experiments.Get(ctx, d, e.Name)
			if err != nil {
				evalFail(w, err, "experiment")
				return
			}
			runs, err := experiments.Runs(ctx, d, e.ID)
			if err != nil {
				evalFail(w, err, "experiment runs")
				return
			}
			v := experimentView{Experiment: e, Runs: len(runs), Evals: len(full.Evals)}
			for i := range runs {
				run := runs[i]
				switch run.Status {
				case experiments.StatusPassed:
					v.RunsPassed++
				case experiments.StatusFailed:
					v.RunsFailed++
				}
				v.CostUSD += run.CostUSD
				out.Totals.Executions += run.TotalEvals
				out.Totals.Passed += run.PassedEvals
				out.Totals.Failed += run.FailedEvals
				v.LastRun = &run
			}
			out.Totals.Runs += len(runs)
			for _, ev := range full.Evals {
				distinct[ev] = true
			}
			out.Totals.CostUSD += v.CostUSD
			out.Experiments = append(out.Experiments, v)
		}
		out.Totals.Evals = len(distinct)
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// pathID parses the {id} path value.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httputil.Error(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

// EvalExperiment serves GET /admin/api/evals/experiments/{id}: the experiment with its evals and its runs (newest first).
func EvalExperiment(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		list, err := experiments.List(ctx, d)
		if err != nil {
			evalFail(w, err, "experiment")
			return
		}
		for _, e := range list {
			if e.ID != id {
				continue
			}
			full, err := experiments.Get(ctx, d, e.Name)
			if err != nil {
				evalFail(w, err, "experiment")
				return
			}
			runs, err := experiments.Runs(ctx, d, id)
			if err != nil {
				evalFail(w, err, "experiment runs")
				return
			}
			for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
				runs[i], runs[j] = runs[j], runs[i]
			}
			if runs == nil {
				runs = []experiments.Run{}
			}
			httputil.WriteJSON(w, http.StatusOK, experimentDetail{Experiment: *full, Runs: runs})
			return
		}
		httputil.Error(w, http.StatusNotFound, "experiment not found")
	}
}

// EvalHistory serves GET /admin/api/evals/experiments/{id}/history?suite=..&name=..: the status of one eval in each run
// of the experiment, oldest run first.
func EvalHistory(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		suite, name := r.URL.Query().Get("suite"), r.URL.Query().Get("name")
		if suite == "" || name == "" {
			httputil.Error(w, http.StatusBadRequest, "suite and name are required")
			return
		}
		points, err := experiments.EvalHistory(r.Context(), d, id, suite, name)
		if err != nil {
			evalFail(w, err, "eval history")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, evalHistory{Suite: suite, Name: name, Points: points})
	}
}

// EvalRun serves GET /admin/api/evals/runs/{id}: the run with its evals and their trajectory references.
func EvalRun(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, err := experiments.GetRun(r.Context(), d, id)
		if err != nil {
			evalFail(w, err, "run")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, run)
	}
}

// EvalResultGraph serves GET /admin/api/evals/results/{id}/graph: the knowledge graph the eval built, read from
// the dataset the eval ran in (same JSON as /admin/api/graph).
func EvalResultGraph(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		ds, err := experiments.ResultDataset(r.Context(), d, id)
		if err != nil {
			evalFail(w, err, "graph")
			return
		}
		g, err := memory.ExportDatasetGraph(r.Context(), d, ds)
		if err != nil {
			evalFail(w, err, "graph")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, g)
	}
}

// EvalTrajectory serves GET /admin/api/evals/trajectories/{id}: the ATIF trajectory JSON of a trajectory that an
// experiment recorded. The ID must be one of the recorded references; the file is looked up in the trajectory
// directory dir first (when not empty), then at the path stored with the reference.
func EvalTrajectory(d *sql.DB, dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ref, err := experiments.FindTrajectory(r.Context(), d, r.PathValue("id"))
		if err != nil {
			evalFail(w, err, "trajectory")
			return
		}
		data, err := readTrajectoryFile(ref, dir)
		if err != nil {
			httputil.Error(w, http.StatusNotFound, "trajectory file not found")
			return
		}
		if !json.Valid(data) {
			log.WithField("trajectory_id", ref.TrajectoryID).Error("admin: stored trajectory is not valid JSON")
			httputil.Error(w, http.StatusInternalServerError, "stored trajectory is corrupt")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

// readTrajectoryFile reads the JSON file of a recorded trajectory: from the trajectory directory dir first (when not
// empty), then from the path stored with the reference. It fails if neither can be read.
func readTrajectoryFile(ref *experiments.Trajectory, dir string) ([]byte, error) {
	var candidates []string
	if dir != "" {
		candidates = append(candidates, filepath.Join(dir, filepath.Base(ref.TrajectoryID)+".json"))
	}
	if ref.Path != "" {
		candidates = append(candidates, ref.Path)
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			return data, nil
		}
	}
	return nil, os.ErrNotExist
}
