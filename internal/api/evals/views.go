package evals

import (
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
)

// The views are the JSON of the evals API (the user routes under /v1/gb/evals and the admin routes under /v1/gb/admin/api/evals, which
// show the same views over all users). They carry the model's own words, not statuses made from them: a run and a case run have the
// core.Status (pending, running, completed, errored, cancelled), a case run also its core.Verdict (pass, fail, or none) and score, and
// a run has its core.Counts.

// RunView is an eval run. Cases is set by the run detail only.
type RunView struct {
	ID     int64       `json:"id"`
	Eval   string      `json:"eval"`
	Status core.Status `json:"status"`
	// Error is why the run errored (Status errored only).
	Error  string         `json:"error,omitempty"`
	Params map[string]any `json:"params"`
	// Counts are those of the run's case runs, as the model has them.
	Counts          core.Counts   `json:"counts"`
	CostUSD         float64       `json:"cost_usd"`
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      *time.Time    `json:"finished_at,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	CancelRequested bool          `json:"cancel_requested"`
	Cases           []CaseRunView `json:"cases,omitempty"`
}

// CaseRunView is the outcome of one case of an eval in one run.
type CaseRunView struct {
	ID   int64  `json:"id"`
	Eval string `json:"eval"`
	Name string `json:"name"`
	// Status is the lifecycle (did it run to the end), Verdict the judgement of a completed one (pass or fail; none otherwise).
	Status       core.Status  `json:"status"`
	Verdict      core.Verdict `json:"verdict,omitempty"`
	Score        *float64     `json:"score,omitempty"`
	Answer       string       `json:"answer,omitempty"`
	Problems     []string     `json:"problems"`
	PassedChecks []string     `json:"passed_checks"`
	Error        string       `json:"error,omitempty"`
	Steps        int          `json:"steps"`
	CostUSD      float64      `json:"cost_usd"`
	StartedAt    time.Time    `json:"started_at"`
	FinishedAt   time.Time    `json:"finished_at"`
	DatasetID    *int64       `json:"dataset_id,omitempty"`
	// Rerun is set when the case run is a rerun with other models (see Rerun): the case run it repeated and the models.
	Rerun        *RerunView       `json:"rerun,omitempty"`
	Trajectories []TrajectoryView `json:"trajectories,omitempty"`
}

// TrajectoryView references the ATIF trajectory of one remember or query call of a case.
type TrajectoryView struct {
	Seq          int            `json:"seq"` // from 1
	Kind         string         `json:"kind"`
	TrajectoryID string         `json:"trajectory_id"`
	AgentName    string         `json:"agent_name"`
	Steps        int            `json:"steps"`
	CostUSD      float64        `json:"cost_usd"`
	ArtifactIDs  []int64        `json:"artifact_ids"`
	Artifacts    []ArtifactView `json:"artifacts"`
	CreatedAt    time.Time      `json:"created_at"`
}

// ArtifactView is an artifact of the dataset a case ran in.
type ArtifactView struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	ValidAt   time.Time `json:"valid_at"`
	CreatedAt time.Time `json:"created_at"`
}

// RerunView says of a case run that it is a rerun: the case run it repeated and the models and reasoning levels it was given (empty:
// the stages kept the ones they had).
type RerunView struct {
	Of                int64  `json:"of"`
	RememberModel     string `json:"remember_model,omitempty"`
	QueryModel        string `json:"query_model,omitempty"`
	RememberReasoning string `json:"remember_reasoning,omitempty"`
	QueryReasoning    string `json:"query_reasoning,omitempty"`
}

// CaseHistory is the progression of one case over the runs of an eval.
type CaseHistory struct {
	Eval   string      `json:"eval"`
	Name   string      `json:"name"`
	Points []CasePoint `json:"points"`
}

// CasePoint is what is plotted of one result of a case in the history of that case: one dot per run.
type CasePoint struct {
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

// Totals are the counts over the evals of a list.
type Totals struct {
	Evals int `json:"evals"`
	Runs  int `json:"runs"`
	// Cases is the number of cases of all the evals.
	Cases int `json:"cases"`
	// Counts are the counts of all the runs added up (Total is the number of case runs the runs were to execute).
	Counts  core.Counts `json:"counts"`
	CostUSD float64     `json:"cost_usd"`
}

// EvalView is an eval with its counts, as listed.
type EvalView struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Cases       int    `json:"cases"`
	Runs        int    `json:"runs"`
	// RunsByStatus is the number of runs with each status (only those that occur).
	RunsByStatus map[core.Status]int `json:"runs_by_status"`
	CostUSD      float64             `json:"cost_usd"`
	// LastRun is the newest run (nil if the eval has none).
	LastRun   *RunView  `json:"last_run,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// Origin says who wrote the cases: "human" or "synthetic" (generated by an LLM, see Synth).
	Origin core.Origin `json:"origin"`
	// Synth is the summary of the definition of a synthetic eval (nil for a human one).
	Synth *SynthInfo `json:"synth,omitempty"`
}

// SynthInfo is what the eval list shows of a synthetic eval: its definition and how its last generation went.
type SynthInfo struct {
	Version     int            `json:"version"`
	Model       string         `json:"model"`
	NumExamples int            `json:"num_examples"`
	Mode        core.SynthMode `json:"mode"`
	// Generation is the status of the newest generation ("" if it has none).
	Generation core.SynthGenerationStatus `json:"generation,omitempty"`
	// GenerationError says why it errored.
	GenerationError string `json:"generation_error,omitempty"`
}

// AddCounts adds the counts of b to those of a.
func AddCounts(a *core.Counts,
	b core.Counts) {
	a.Total += b.Total
	a.Pending += b.Pending
	a.Running += b.Running
	a.Completed += b.Completed
	a.Errored += b.Errored
	a.Cancelled += b.Cancelled
	a.Passed += b.Passed
	a.Failed += b.Failed
}

// ViewOfRun is the view of eval run r of the eval called eval.
func ViewOfRun(r *core.EvalRun,
	eval string) RunView {
	v := RunView{
		ID:              int64(r.ID),
		Eval:            eval,
		Status:          r.Status,
		Error:           r.Error,
		Params:          map[string]any(r.Params),
		Counts:          r.Counts,
		CostUSD:         r.CostUSD,
		StartedAt:       TimeOr(r.StartedAt, r.CreatedAt),
		FinishedAt:      r.FinishedAt,
		CreatedAt:       r.CreatedAt,
		CancelRequested: r.CancelRequested,
	}
	if v.Params == nil {
		v.Params = map[string]any{}
	}
	return v
}

// KnowledgeMeta is the knowledge metadata of a case run (empty if it has none).
func KnowledgeMeta(c *core.CaseRun) *core.KnowledgeCaseRunMeta {
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

// TimeOr is *t, or fallback if t is nil.
func TimeOr(t *time.Time,
	fallback time.Time) time.Time {
	if t != nil {
		return *t
	}
	return fallback
}

// OrEmpty is s, or an empty (not nil) slice if s is nil, so that it is a JSON [] and not null.
func OrEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ViewOfCase is the view of a case run; the trajectories (with their artifacts) are added by the caller that needs them.
func ViewOfCase(c *core.CaseRun,
	eval,
	name string) CaseRunView {
	m := KnowledgeMeta(c)
	return CaseRunView{
		ID:           int64(c.ID),
		Eval:         eval,
		Name:         name,
		Status:       c.Status,
		Verdict:      c.Verdict,
		Score:        c.Score,
		Answer:       m.Answer,
		Problems:     OrEmpty(m.Problems),
		PassedChecks: OrEmpty(m.PassedChecks),
		Error:        c.Error,
		Steps:        m.Steps,
		CostUSD:      c.CostUSD,
		StartedAt:    TimeOr(c.StartedAt, c.CreatedAt),
		FinishedAt:   TimeOr(c.FinishedAt, c.CreatedAt),
		DatasetID:    m.DatasetID,
	}
}

// RerunOf reads, from the params of the eval run of a case run, whether it is a rerun; nil if it is not.
func RerunOf(p core.Params) *RerunView {
	var of int64
	switch v := p[core.ParamRerunOf].(type) {
	case int64:
		of = v
	case int:
		of = int64(v)
	case float64:
		of = int64(v)
	}
	if of == 0 {
		return nil
	}
	rememberModel, _ := p[core.ParamRememberModel].(string)
	queryModel, _ := p[core.ParamQueryModel].(string)
	rememberReasoning, _ := p[core.ParamRememberReasoning].(string)
	queryReasoning, _ := p[core.ParamQueryReasoning].(string)
	return &RerunView{
		Of:                of,
		RememberModel:     rememberModel,
		QueryModel:        queryModel,
		RememberReasoning: rememberReasoning,
		QueryReasoning:    queryReasoning,
	}
}

// HistoryPoints keeps of the results of a case (its history) only what is plotted, and adds when the run of each started. Results of
// runs not in runs are dropped. It leaves out what the history does not need: the answer, the dataset, the trajectories and the cost.
func HistoryPoints(results []core.CaseRun,
	runs []core.EvalRun) []CasePoint {
	started := make(map[core.EvalRunID]time.Time, len(runs))
	for _, r := range runs {
		started[r.ID] = TimeOr(r.StartedAt, r.CreatedAt)
	}
	out := make([]CasePoint,
		0,
		len(results))
	for i := range results {
		at, ok := started[results[i].EvalRunID]
		if !ok {
			continue
		}
		v := ViewOfCase(&results[i],
			"",
			"")
		out = append(out, CasePoint{
			RunID:        int64(results[i].EvalRunID),
			RunStartedAt: at,
			Status:       v.Status,
			Verdict:      v.Verdict,
			Score:        v.Score,
			Problems:     v.Problems,
			PassedChecks: v.PassedChecks,
			Error:        v.Error,
			Steps:        v.Steps,
			StartedAt:    v.StartedAt,
			FinishedAt:   v.FinishedAt,
		})
	}
	return out
}
