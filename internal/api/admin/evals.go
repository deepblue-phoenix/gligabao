package admin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/evals/experiments"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	log "github.com/sirupsen/logrus"
)

// EvalTotals are the case counts over all experiments.
type EvalTotals struct {
	Experiments int `json:"experiments"`
	Runs        int `json:"runs"`
	// Cases is the number of distinct cases (eval + case name) any experiment executed (counted once across experiments).
	Cases int `json:"cases"`
	// Executions is the number of case results over all finished runs. Passed, Failed (a check did not hold), Errors (could not
	// be run to the end) and Judged (training: scored) count the cases with that status; the rest were not reached.
	Executions int     `json:"executions"`
	Passed     int     `json:"passed"`
	Failed     int     `json:"failed"`
	Errors     int     `json:"errors"`
	Judged     int     `json:"judged"`
	CostUSD    float64 `json:"cost_usd"`
}

// experimentView is an experiment with its counts, as listed in the admin UI.
type experimentView struct {
	experiments.Experiment
	Runs       int     `json:"runs"`
	Cases      int     `json:"cases"`
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

// caseHistory is the progression of one case over the runs of an experiment.
type caseHistory struct {
	Eval   string      `json:"eval"`
	Name   string      `json:"name"`
	Points []casePoint `json:"points"`
}

// casePoint is what the admin UI plots of one result of a case in the history of that case: one dot per run.
type casePoint struct {
	RunID        int64     `json:"run_id"`
	RunStartedAt time.Time `json:"run_started_at"`
	Status       string    `json:"status"`
	Problems     []string  `json:"problems"`
	PassedChecks []string  `json:"passed_checks"`
	Error        string    `json:"error,omitempty"`
	Steps        int       `json:"steps"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
}

// historyPoints keeps of the results of a case (its history) only what the UI plots, and adds when the run of each
// started. Results of runs not in runs are dropped. It leaves out what the history does not need: the answer, the dataset,
// the trajectories and the cost.
func historyPoints(results []experiments.CaseResult, runs []experiments.Run) []casePoint {
	started := make(map[int64]time.Time, len(runs))
	for _, r := range runs {
		started[r.ID] = r.StartedAt
	}
	out := make([]casePoint, 0, len(results))
	for _, e := range results {
		at, ok := started[e.RunID]
		if !ok {
			continue
		}
		out = append(out, casePoint{
			RunID: e.RunID, RunStartedAt: at, Status: e.Status, Problems: e.Problems, PassedChecks: e.PassedChecks,
			Error: e.Error, Steps: e.Steps, StartedAt: e.StartedAt, FinishedAt: e.FinishedAt,
		})
	}
	return out
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
		distinct := map[experiments.CaseRef]bool{}
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
			v := experimentView{Experiment: e, Runs: len(runs), Cases: len(full.Cases)}
			for i := range runs {
				run := runs[i]
				switch run.Status {
				case experiments.StatusPassed:
					v.RunsPassed++
				case experiments.StatusFailed:
					v.RunsFailed++
				}
				v.CostUSD += run.CostUSD
				out.Totals.Executions += run.TotalCases
				out.Totals.Passed += run.PassedCases
				out.Totals.Failed += run.FailedCases
				out.Totals.Errors += run.ErrorCases
				out.Totals.Judged += run.JudgedCases
				v.LastRun = &run
			}
			out.Totals.Runs += len(runs)
			for _, c := range full.Cases {
				distinct[c] = true
			}
			out.Totals.CostUSD += v.CostUSD
			out.Experiments = append(out.Experiments, v)
		}
		out.Totals.Cases = len(distinct)
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

// EvalExperiment serves GET /admin/api/evals/experiments/{id}: the experiment with its cases and its runs (newest first).
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

// EvalCaseHistory serves GET /admin/api/evals/experiments/{id}/history?eval=..&name=..: the status of one case in each run
// of the experiment, oldest run first.
func EvalCaseHistory(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		eval, name := r.URL.Query().Get("eval"), r.URL.Query().Get("name")
		if eval == "" || name == "" {
			httputil.Error(w, http.StatusBadRequest, "eval and name are required")
			return
		}
		results, err := experiments.CaseHistory(r.Context(), d, id, eval, name)
		if err != nil {
			evalFail(w, err, "case history")
			return
		}
		runs, err := experiments.Runs(r.Context(), d, id)
		if err != nil {
			evalFail(w, err, "experiment runs")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, caseHistory{Eval: eval, Name: name, Points: historyPoints(results, runs)})
	}
}

// EvalRun serves GET /admin/api/evals/runs/{id}: the run with its cases and their trajectory references.
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

// EvalCaseResultGraph serves GET /admin/api/evals/case_results/{id}/graph: the knowledge graph the case built, read from
// the dataset the case ran in (same JSON as /admin/api/graph).
func EvalCaseResultGraph(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		ds, err := experiments.CaseResultDataset(r.Context(), d, id)
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
