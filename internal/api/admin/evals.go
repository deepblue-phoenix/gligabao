package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals_neo/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	log "github.com/sirupsen/logrus"
)

// The evals views of the admin UI show the knowledge evals of internal/evals_neo: an experiment (core.ExperimentDef), its runs
// (core.ExperimentRun) and, in a run, the case runs of its knowledge eval runs, each with its trajectories. The JSON is what the UI
// (admin_ui/src/evals.ts) reads. It carries the model's own words, not statuses made from them: a run and a case run have the
// core.Status (pending, running, completed, errored, cancelled), a case run also its core.Verdict (pass, fail, or none) and score,
// and a run has its core.Counts.

// EvalTotals are the case counts over all experiments.
type EvalTotals struct {
	Experiments int `json:"experiments"`
	Runs        int `json:"runs"`
	// Cases is the number of distinct cases (eval + case name) any experiment executed (counted once across experiments).
	Cases int `json:"cases"`
	// Counts are the counts of all the runs added up (Total is the number of case runs the runs were to execute).
	Counts  core.Counts `json:"counts"`
	CostUSD float64     `json:"cost_usd"`
}

// addCounts adds the counts of b to those of a.
func addCounts(a *core.Counts, b core.Counts) {
	a.Total += b.Total
	a.Pending += b.Pending
	a.Running += b.Running
	a.Completed += b.Completed
	a.Errored += b.Errored
	a.Cancelled += b.Cancelled
	a.Passed += b.Passed
	a.Failed += b.Failed
}

// experimentRunView is a run of an experiment, as listed in the admin UI.
type experimentRunView struct {
	ID           int64       `json:"id"`
	ExperimentID int64       `json:"experiment_id"`
	Status       core.Status `json:"status"`
	// Error is why the run errored (Status errored only).
	Error  string         `json:"error,omitempty"`
	Params map[string]any `json:"params"`
	// Counts are those of the run's case runs, as the model has them.
	Counts     core.Counts   `json:"counts"`
	CostUSD    float64       `json:"cost_usd"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt *time.Time    `json:"finished_at,omitempty"`
	CaseRuns   []caseRunView `json:"case_runs,omitempty"` // the run's only
}

func viewOfRun(r *core.ExperimentRun) experimentRunView {
	v := experimentRunView{ID: int64(r.ID), ExperimentID: int64(r.DefID), Status: r.Status, Error: r.Error, Params: map[string]any(r.Params),
		Counts: r.Counts, CostUSD: r.CostUSD, StartedAt: r.CreatedAt, FinishedAt: r.FinishedAt}
	if r.StartedAt != nil {
		v.StartedAt = *r.StartedAt
	}
	if v.Params == nil {
		v.Params = map[string]any{}
	}
	return v
}

// experimentFields are the fields of an experiment the views share.
type experimentFields struct {
	ID          int64          `json:"id"`
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Config      map[string]any `json:"config"`
	CreatedAt   time.Time      `json:"created_at"`
}

func experimentOf(d *core.ExperimentDef) experimentFields {
	f := experimentFields{ID: int64(d.ID), Type: string(core.TypeKnowledge), Name: d.Name, Description: d.Description, Config: map[string]any(d.Params), CreatedAt: d.CreatedAt}
	if f.Config == nil {
		f.Config = map[string]any{}
	}
	return f
}

// experimentView is an experiment with its counts, as listed in the admin UI.
type experimentView struct {
	experimentFields
	Runs  int `json:"runs"`
	Cases int `json:"cases"`
	// RunsByStatus is the number of runs with each status (only those that occur).
	RunsByStatus map[core.Status]int `json:"runs_by_status"`
	CostUSD      float64             `json:"cost_usd"`
	// LastRun is the newest run (nil if the experiment has none).
	LastRun *experimentRunView `json:"last_run,omitempty"`
}

type evalOverview struct {
	Totals      EvalTotals       `json:"totals"`
	Experiments []experimentView `json:"experiments"`
}

type experimentDetail struct {
	experimentFields
	Cases []core.CaseRef `json:"cases,omitempty"`
	// Runs are the experiment's runs, newest first.
	Runs []experimentRunView `json:"runs"`
}

// caseRunView is the outcome of one case of an eval in one run.
type caseRunView struct {
	ID   int64  `json:"id"`
	Eval string `json:"eval"`
	Name string `json:"name"`
	// Status is the lifecycle (did it run to the end), Verdict the judgement of a completed one (pass or fail; none otherwise).
	Status       core.Status      `json:"status"`
	Verdict      core.Verdict     `json:"verdict,omitempty"`
	Score        *float64         `json:"score,omitempty"`
	Answer       string           `json:"answer,omitempty"`
	Problems     []string         `json:"problems"`
	PassedChecks []string         `json:"passed_checks"`
	Error        string           `json:"error,omitempty"`
	Steps        int              `json:"steps"`
	CostUSD      float64          `json:"cost_usd"`
	StartedAt    time.Time        `json:"started_at"`
	FinishedAt   time.Time        `json:"finished_at"`
	DatasetID    *int64           `json:"dataset_id,omitempty"`
	Trajectories []trajectoryView `json:"trajectories,omitempty"`
}

// trajectoryView references the ATIF trajectory of one remember or recall call of a case.
type trajectoryView struct {
	Seq          int            `json:"seq"` // from 1
	Kind         string         `json:"kind"`
	TrajectoryID string         `json:"trajectory_id"`
	AgentName    string         `json:"agent_name"`
	Steps        int            `json:"steps"`
	CostUSD      float64        `json:"cost_usd"`
	ArtifactIDs  []int64        `json:"artifact_ids"`
	Artifacts    []artifactView `json:"artifacts"`
	CreatedAt    time.Time      `json:"created_at"`
}

// artifactView is an artifact of the dataset a case ran in.
type artifactView struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	ValidAt   time.Time `json:"valid_at"`
	CreatedAt time.Time `json:"created_at"`
}

func knowledgeMeta(c *core.CaseRun) *core.KnowledgeCaseRunMeta {
	switch m := c.Meta.(type) {
	case *core.KnowledgeCaseRunMeta:
		if m != nil {
			return m
		}
	case core.KnowledgeCaseRunMeta:
		return &m
	}
	return &core.KnowledgeCaseRunMeta{}
}

func timeOr(t *time.Time, fallback time.Time) time.Time {
	if t != nil {
		return *t
	}
	return fallback
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// viewOfCase is the view of a case run; the trajectories (with their artifacts) are added by the caller that needs them.
func viewOfCase(c *core.CaseRun, eval, name string) caseRunView {
	m := knowledgeMeta(c)
	return caseRunView{ID: int64(c.ID), Eval: eval, Name: name, Status: c.Status, Verdict: c.Verdict, Score: c.Score, Answer: m.Answer, Problems: orEmpty(m.Problems),
		PassedChecks: orEmpty(m.PassedChecks), Error: c.Error, Steps: m.Steps, CostUSD: c.CostUSD, StartedAt: timeOr(c.StartedAt, c.CreatedAt),
		FinishedAt: timeOr(c.FinishedAt, c.CreatedAt), DatasetID: m.DatasetID}
}

// knowledgeCaseRun is a case run of a knowledge eval with the names of its eval and its case.
type knowledgeCaseRun struct {
	Eval, Name string
	Run        core.CaseRun
}

// knowledgeCaseRuns returns the case runs of the knowledge evals of experiment run id, with their metadata and trajectories, in
// the order of the eval runs and of the cases.
func knowledgeCaseRuns(ctx context.Context, d *sql.DB, s evalsdb.Store, id core.ExperimentRunID) ([]knowledgeCaseRun, error) {
	evalRuns, err := s.ListEvalRunsOfExperimentRun(ctx, d, id)
	if err != nil {
		return nil, err
	}
	var out []knowledgeCaseRun
	for _, er := range evalRuns {
		if er.Type != core.TypeKnowledge {
			continue
		}
		def, err := s.GetEvalDef(ctx, d, er.DefID)
		if err != nil {
			return nil, err
		}
		cases, err := s.ListCaseDefs(ctx, d, er.DefID)
		if err != nil {
			return nil, err
		}
		names := make(map[core.CaseDefID]string, len(cases))
		for _, c := range cases {
			names[c.ID] = c.Name
		}
		runs, err := s.ListCaseRuns(ctx, d, er.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range runs {
			out = append(out, knowledgeCaseRun{Eval: def.Name, Name: names[c.DefID], Run: c})
		}
	}
	return out, nil
}

// experimentKinds says of an experiment def whether it is one of the knowledge evals: it has a knowledge eval, or none yet.
type experimentKinds struct {
	ctx   context.Context
	d     *sql.DB
	s     evalsdb.Store
	types map[core.EvalDefID]core.Type
}

func (k *experimentKinds) isKnowledge(def *core.ExperimentDef) (bool, error) {
	if len(def.EvalDefIDs) == 0 {
		return true, nil
	}
	for _, id := range def.EvalDefIDs {
		t, ok := k.types[id]
		if !ok {
			e, err := k.s.GetEvalDef(k.ctx, k.d, id)
			if err != nil {
				return false, err
			}
			t = e.Type
			k.types[id] = t
		}
		if t == core.TypeKnowledge {
			return true, nil
		}
	}
	return false, nil
}

// knowledgeExperiments returns the experiment defs of the knowledge evals, oldest first.
func knowledgeExperiments(ctx context.Context, d *sql.DB, s evalsdb.Store) ([]core.ExperimentDef, error) {
	all, err := s.ListExperimentDefs(ctx, d)
	if err != nil {
		return nil, err
	}
	k := &experimentKinds{ctx, d, s, map[core.EvalDefID]core.Type{}}
	var out []core.ExperimentDef
	for i := range all {
		ok, err := k.isKnowledge(&all[i])
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, all[i])
		}
	}
	return out, nil
}

func evalFail(w http.ResponseWriter, err error, what string) {
	if errors.Is(err, evalsdb.ErrNotFound) {
		httputil.Error(w, http.StatusNotFound, what+" not found")
		return
	}
	log.WithError(err).Errorf("admin: %s failed", what)
	httputil.Error(w, http.StatusInternalServerError, "failed to load "+what)
}

// EvalOverview serves GET /admin/api/evals: every experiment with its counts, and the totals over all of them.
func EvalOverview(d *sql.DB, s evalsdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		list, err := knowledgeExperiments(ctx, d, s)
		if err != nil {
			evalFail(w, err, "experiments")
			return
		}
		out := evalOverview{Experiments: make([]experimentView, 0, len(list))}
		out.Totals.Experiments = len(list)
		distinct := map[core.CaseRef]bool{}
		for i := range list {
			cases, err := s.ListExperimentCases(ctx, d, list[i].ID)
			if err != nil {
				evalFail(w, err, "experiment")
				return
			}
			runs, err := s.ListExperimentRuns(ctx, d, list[i].ID)
			if err != nil {
				evalFail(w, err, "experiment runs")
				return
			}
			v := experimentView{experimentFields: experimentOf(&list[i]), Runs: len(runs), Cases: len(cases), RunsByStatus: map[core.Status]int{}}
			for j := range runs {
				run := viewOfRun(&runs[j])
				v.RunsByStatus[run.Status]++
				v.CostUSD += run.CostUSD
				addCounts(&out.Totals.Counts, run.Counts)
				v.LastRun = &run
			}
			out.Totals.Runs += len(runs)
			for _, c := range cases {
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
func EvalExperiment(d *sql.DB, s evalsdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		def, err := s.GetExperimentDef(ctx, d, core.ExperimentDefID(id))
		if err != nil {
			evalFail(w, err, "experiment")
			return
		}
		cases, err := s.ListExperimentCases(ctx, d, def.ID)
		if err != nil {
			evalFail(w, err, "experiment")
			return
		}
		runs, err := s.ListExperimentRuns(ctx, d, def.ID)
		if err != nil {
			evalFail(w, err, "experiment runs")
			return
		}
		out := experimentDetail{experimentFields: experimentOf(def), Cases: cases, Runs: make([]experimentRunView, len(runs))}
		for i := range runs {
			out.Runs[len(runs)-1-i] = viewOfRun(&runs[i])
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// caseHistory is the progression of one case over the runs of an experiment.
type caseHistory struct {
	Eval   string      `json:"eval"`
	Name   string      `json:"name"`
	Points []casePoint `json:"points"`
}

// casePoint is what the admin UI plots of one result of a case in the history of that case: one dot per run.
type casePoint struct {
	RunID        int64        `json:"run_id"`
	RunStartedAt time.Time    `json:"run_started_at"`
	Status       core.Status  `json:"status"`
	Verdict      core.Verdict `json:"verdict,omitempty"`
	Score        *float64     `json:"score,omitempty"`
	Problems     []string     `json:"problems"`
	PassedChecks []string     `json:"passed_checks"`
	Error        string       `json:"error,omitempty"`
	Steps        int          `json:"steps"`
	StartedAt    time.Time    `json:"started_at"`
	FinishedAt   time.Time    `json:"finished_at"`
}

// historyPoints keeps of the results of a case (its history) only what the UI plots, and adds when the run of each
// started. Results of runs not in runs are dropped. It leaves out what the history does not need: the answer, the dataset,
// the trajectories and the cost.
func historyPoints(results []evalsdb.CaseHistoryEntry, runs []core.ExperimentRun) []casePoint {
	started := make(map[core.ExperimentRunID]time.Time, len(runs))
	for _, r := range runs {
		started[r.ID] = timeOr(r.StartedAt, r.CreatedAt)
	}
	out := make([]casePoint, 0, len(results))
	for i := range results {
		at, ok := started[results[i].ExperimentRunID]
		if !ok {
			continue
		}
		v := viewOfCase(&results[i].CaseRun, "", "")
		out = append(out, casePoint{
			RunID: int64(results[i].ExperimentRunID), RunStartedAt: at, Status: v.Status, Verdict: v.Verdict, Score: v.Score, Problems: v.Problems, PassedChecks: v.PassedChecks,
			Error: v.Error, Steps: v.Steps, StartedAt: v.StartedAt, FinishedAt: v.FinishedAt,
		})
	}
	return out
}

// EvalCaseHistory serves GET /admin/api/evals/experiments/{id}/history?eval=..&name=..: the status of one case in each run
// of the experiment, oldest run first.
func EvalCaseHistory(d *sql.DB, s evalsdb.Store) http.HandlerFunc {
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
		results, err := s.ListCaseHistory(r.Context(), d, core.ExperimentDefID(id), eval, name)
		if err != nil {
			evalFail(w, err, "case history")
			return
		}
		runs, err := s.ListExperimentRuns(r.Context(), d, core.ExperimentDefID(id))
		if err != nil {
			evalFail(w, err, "experiment runs")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, caseHistory{Eval: eval, Name: name, Points: historyPoints(results, runs)})
	}
}

// EvalRun serves GET /admin/api/evals/runs/{id}: the run with its cases and their trajectory references. The artifacts of each
// trajectory are read from the dataset its case ran in.
func EvalRun(d *sql.DB, s evalsdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		run, err := s.GetExperimentRun(ctx, d, core.ExperimentRunID(id))
		if err != nil {
			evalFail(w, err, "run")
			return
		}
		cases, err := knowledgeCaseRuns(ctx, d, s, run.ID)
		if err != nil {
			evalFail(w, err, "run")
			return
		}
		out := viewOfRun(run)
		out.CaseRuns = make([]caseRunView, 0, len(cases))
		for i := range cases {
			v, err := viewWithTrajectories(ctx, d, &cases[i])
			if err != nil {
				evalFail(w, err, "run")
				return
			}
			out.CaseRuns = append(out.CaseRuns, v)
		}
		httputil.WriteJSON(w, http.StatusOK, out)
	}
}

// viewWithTrajectories is the view of a case run with its trajectories and their artifacts.
func viewWithTrajectories(ctx context.Context, d *sql.DB, c *knowledgeCaseRun) (caseRunView, error) {
	v := viewOfCase(&c.Run, c.Eval, c.Name)
	m := knowledgeMeta(&c.Run)
	var all map[int64]graph.Artifact // the dataset's artifacts, loaded when a trajectory needs them
	for _, t := range m.Trajectories {
		tv := trajectoryView{Seq: t.Seq + 1, Kind: t.Kind, TrajectoryID: t.TrajectoryID, AgentName: t.AgentName, Steps: t.Steps, CostUSD: t.CostUSD,
			ArtifactIDs: orEmpty(t.ArtifactIDs), Artifacts: []artifactView{}, CreatedAt: t.CreatedAt}
		if len(t.ArtifactIDs) > 0 && m.DatasetID != nil {
			if all == nil {
				list, err := graph.ListArtifacts(ctx, d, *m.DatasetID)
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
					tv.Artifacts = append(tv.Artifacts, artifactView{ID: a.ID, Kind: a.Kind, Text: a.Text, ValidAt: a.ValidAt, CreatedAt: a.CreatedAt})
				}
			}
		}
		v.Trajectories = append(v.Trajectories, tv)
	}
	return v, nil
}

// EvalCaseRunGraph serves GET /admin/api/evals/case_runs/{id}/graph: the knowledge graph the case built, read from
// the dataset the case ran in (same JSON as /admin/api/graph).
func EvalCaseRunGraph(d *sql.DB, s evalsdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		c, err := s.GetCaseRun(ctx, d, core.CaseRunID(id))
		if err == nil && (c.Type != core.TypeKnowledge || knowledgeMeta(c).DatasetID == nil) {
			err = evalsdb.ErrNotFound
		}
		if err != nil {
			evalFail(w, err, "graph")
			return
		}
		all, err := relational.ListAllDatasets(ctx, d, true)
		if err != nil {
			evalFail(w, err, "graph")
			return
		}
		i := slices.IndexFunc(all, func(ds relational.Dataset) bool { return ds.ID == *knowledgeMeta(c).DatasetID })
		if i < 0 {
			evalFail(w, evalsdb.ErrNotFound, "graph")
			return
		}
		g, err := memory.ExportDatasetGraph(ctx, d, &all[i])
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
func EvalTrajectory(d *sql.DB, s evalsdb.Store, dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ref, err := s.FindKnowledgeTrajectory(r.Context(), d, r.PathValue("id"))
		if err != nil {
			evalFail(w, err, "trajectory")
			return
		}
		data, err := readTrajectoryFile(&ref.KnowledgeTrajectory, dir)
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
func readTrajectoryFile(ref *core.KnowledgeTrajectory, dir string) ([]byte, error) {
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
