// Package readstest is the test of the read functions of the Store (db.Reads), shared by the tests of the two backends: the same
// records are written through the Store and read back.
package readstest

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Run writes defs and runs of both types through s into d (a database with the schema applied) and reads them back.
func Run(t *testing.T, d *sql.DB, s db.Store) {
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	// Defs: a knowledge eval of two cases and a training one of one, both in one experiment.
	kn := &core.EvalDef{Name: "kn", Type: core.TypeKnowledge, Tags: []string{"a"}, Params: core.Params{"judge": "x"}}
	must(t, s.CreateEvalDef(ctx, d, kn))
	tr := &core.EvalDef{Name: "tr", Type: core.TypeTraining}
	must(t, s.CreateEvalDef(ctx, d, tr))
	var kcases []*core.CaseDef
	for i, name := range []string{"c0", "c1"} {
		c := &core.CaseDef{EvalDefID: kn.ID, Type: core.TypeKnowledge, Name: name, Position: i}
		must(t, s.CreateCaseDef(ctx, d, c))
		must(t, s.CreateCaseDefMeta(ctx, d, &core.KnowledgeCaseDefMeta{CaseDefID: c.ID}))
		kcases = append(kcases, c)
	}
	tc := &core.CaseDef{EvalDefID: tr.ID, Type: core.TypeTraining, Name: "t0", Position: 0}
	must(t, s.CreateCaseDef(ctx, d, tc))
	must(t, s.CreateCaseDefMeta(ctx, d, &core.TrainingCaseDefMeta{CaseDefID: tc.ID, TrainingInput: core.TrainingInput{Instruction: "q"}, TrainingExpected: core.TrainingExpected{Output: "a"}}))
	xd := &core.ExperimentDef{Name: "x", Description: "d", Params: core.Params{"lr": 0.1}}
	must(t, s.CreateExperimentDef(ctx, d, xd))
	must(t, s.CreateExperimentDefEval(ctx, d, xd.ID, kn.ID, 0))
	must(t, s.CreateExperimentDefEval(ctx, d, xd.ID, tr.ID, 1))
	empty := &core.ExperimentDef{Name: "empty"}
	must(t, s.CreateExperimentDef(ctx, d, empty))

	defs, err := s.ListExperimentDefs(ctx, d)
	must(t, err)
	if len(defs) != 2 || defs[0].Name != "x" || defs[1].Name != "empty" || !reflect.DeepEqual(defs[0].EvalDefIDs, []core.EvalDefID{kn.ID, tr.ID}) ||
		len(defs[1].EvalDefIDs) != 0 || defs[0].Params["lr"] != 0.1 || defs[0].CreatedAt.IsZero() {
		t.Fatalf("experiment defs: %+v", defs)
	}
	if x, err := s.GetExperimentDef(ctx, d, xd.ID); err != nil || x.Name != "x" || len(x.EvalDefIDs) != 2 {
		t.Fatalf("GetExperimentDef: %+v %v", x, err)
	}
	if _, err := s.GetExperimentDef(ctx, d, 999); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetExperimentDef of a missing one: %v", err)
	}
	if e, err := s.GetEvalDef(ctx, d, kn.ID); err != nil || e.Name != "kn" || e.Type != core.TypeKnowledge || !reflect.DeepEqual(e.Tags, []string{"a"}) || e.Params["judge"] != "x" || e.Cases != nil {
		t.Fatalf("GetEvalDef: %+v %v", e, err)
	}
	if _, err := s.GetEvalDef(ctx, d, 999); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetEvalDef of a missing one: %v", err)
	}
	if cs, err := s.ListCaseDefs(ctx, d, kn.ID); err != nil || len(cs) != 2 || cs[0].Name != "c0" || cs[1].Name != "c1" || cs[1].Position != 1 || cs[0].Meta != nil {
		t.Fatalf("ListCaseDefs: %+v %v", cs, err)
	}

	// Two runs of the experiment; each has a knowledge eval run, the first also a training one.
	var xruns []*core.ExperimentRun
	var kruns []*core.EvalRun
	var caseRuns [][]*core.CaseRun // by run, then case
	var tev *core.EvalRun
	for i := 0; i < 2; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		end := at.Add(time.Second)
		xr := &core.ExperimentRun{DefID: xd.ID, Status: core.StatusRunning, Params: core.Params{"i": i}, Timing: core.Timing{CreatedAt: at, StartedAt: &at}}
		must(t, s.CreateExperimentRun(ctx, d, xr))
		xruns = append(xruns, xr)
		kr := &core.EvalRun{DefID: kn.ID, Type: core.TypeKnowledge, ExperimentRunID: &xr.ID, Status: core.StatusCompleted, Params: core.Params{"p": 1},
			Counts: core.Counts{Total: 2, Completed: 2, Passed: 1 + i, Failed: 1 - i}, CostUSD: 0.5, Timing: core.Timing{CreatedAt: at, StartedAt: &at, FinishedAt: &end}}
		must(t, s.CreateEvalRun(ctx, d, kr))
		must(t, s.CreateEvalRunMeta(ctx, d, &core.KnowledgeEvalRunMeta{EvalRunID: kr.ID, Chunks: 3, Steps: 7, RememberCalls: 2, ChecksPassed: 4}))
		kruns = append(kruns, kr)
		var crs []*core.CaseRun
		for j, c := range kcases {
			cr := &core.CaseRun{DefID: c.ID, EvalRunID: kr.ID, Type: core.TypeKnowledge, Status: core.StatusCompleted, Verdict: core.VerdictPass, CostUSD: 0.25,
				Timing: core.Timing{CreatedAt: at, StartedAt: &at, FinishedAt: &end}}
			if j == 1 && i == 0 {
				cr.Verdict = core.VerdictFail
			}
			must(t, s.CreateCaseRun(ctx, d, cr))
			ds := int64(40 + i)
			must(t, s.CreateCaseRunMeta(ctx, d, &core.KnowledgeCaseRunMeta{CaseRunID: cr.ID, Answer: "ans", Problems: []string{"p"}, PassedChecks: []string{"ok"},
				DatasetID: &ds, Steps: 4, Input: core.KnowledgeInput{Query: "q?"}, Trajectories: []core.KnowledgeTrajectory{
					{Seq: 0, Kind: "remember", TrajectoryID: "tr-" + string(rune('a'+i)) + string(rune('0'+j)) + "-0", AgentName: "gligabao-remember", Steps: 2, ArtifactIDs: []int64{5, 6}, Path: "/p/0.json"},
					{Seq: 1, Kind: "recall", TrajectoryID: "tr-" + string(rune('a'+i)) + string(rune('0'+j)) + "-1", AgentName: "gligabao-recall", Steps: 2},
				}}))
			crs = append(crs, cr)
		}
		caseRuns = append(caseRuns, crs)
		if i == 0 {
			tev = &core.EvalRun{DefID: tr.ID, Type: core.TypeTraining, ExperimentRunID: &xr.ID, Status: core.StatusCompleted, Counts: core.Counts{Total: 1, Completed: 1, Passed: 1},
				Timing: core.Timing{CreatedAt: at, StartedAt: &at, FinishedAt: &end}}
			must(t, s.CreateEvalRun(ctx, d, tev))
			must(t, s.CreateEvalRunMeta(ctx, d, &core.TrainingEvalRunMeta{EvalRunID: tev.ID, TrainedModelID: 7, JudgeModel: "j", NumAnswered: 1, NumJudged: 1, NumCorrect: 1, ScoreSum: 5}))
			tcr := &core.CaseRun{DefID: tc.ID, EvalRunID: tev.ID, Type: core.TypeTraining, Status: core.StatusCompleted, Verdict: core.VerdictPass, Score: ptr(1.0),
				Timing: core.Timing{CreatedAt: at, StartedAt: &at, FinishedAt: &end}}
			must(t, s.CreateCaseRun(ctx, d, tcr))
			score := 5
			must(t, s.CreateCaseRunMeta(ctx, d, &core.TrainingCaseRunMeta{CaseRunID: tcr.ID, Instruction: "q", Reference: "a", Response: "a", Batch: 1, JudgeScore: &score, JudgeReason: "r", DurationMS: 12}))
		}
	}

	// The experiment runs, oldest first, with their counts, params and times.
	xs, err := s.ListExperimentRuns(ctx, d, xd.ID)
	must(t, err)
	if len(xs) != 2 || xs[0].ID != xruns[0].ID || xs[1].ID != xruns[1].ID || xs[0].Status != core.StatusRunning || xs[1].Params["i"] != float64(1) ||
		xs[0].StartedAt == nil || !xs[0].StartedAt.Equal(start) || xs[0].FinishedAt != nil || !xs[0].CreatedAt.Equal(start) {
		t.Fatalf("experiment runs: %+v", xs)
	}
	if xs, err := s.ListExperimentRuns(ctx, d, empty.ID); err != nil || xs == nil || len(xs) != 0 {
		t.Fatalf("runs of an experiment without: %v %v", xs, err)
	}
	xruns[0].Status, xruns[0].Counts = core.StatusCompleted, core.Counts{Total: 3, Completed: 3, Passed: 2, Failed: 1}
	must(t, s.UpdateExperimentRun(ctx, d, xruns[0]))
	if x, err := s.GetExperimentRun(ctx, d, xruns[0].ID); err != nil || x.Status != core.StatusCompleted || x.Counts.Passed != 2 || x.Counts.Total != 3 {
		t.Fatalf("GetExperimentRun: %+v %v", x, err)
	}
	if _, err := s.GetExperimentRun(ctx, d, 999); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetExperimentRun of a missing one: %v", err)
	}

	// The eval runs of an experiment run, with the metadata of their type.
	evs, err := s.ListEvalRunsOfExperimentRun(ctx, d, xruns[0].ID)
	must(t, err)
	if len(evs) != 2 || evs[0].ID != kruns[0].ID || evs[1].ID != tev.ID || evs[0].Type != core.TypeKnowledge || evs[0].ExperimentRunID == nil || *evs[0].ExperimentRunID != xruns[0].ID ||
		evs[0].CostUSD != 0.5 || evs[0].Counts.Passed != 1 || evs[0].Params["p"] != float64(1) || evs[0].FinishedAt == nil {
		t.Fatalf("eval runs of an experiment run: %+v", evs)
	}
	if m, ok := evs[0].Meta.(*core.KnowledgeEvalRunMeta); !ok || m.Chunks != 3 || m.Steps != 7 || m.RememberCalls != 2 || m.ChecksPassed != 4 {
		t.Fatalf("knowledge eval run meta: %#v", evs[0].Meta)
	}
	if m, ok := evs[1].Meta.(*core.TrainingEvalRunMeta); !ok || m.TrainedModelID != 7 || m.JudgeModel != "j" || m.NumCorrect != 1 || m.ScoreSum != 5 {
		t.Fatalf("training eval run meta: %#v", evs[1].Meta)
	}
	if all, err := s.ListEvalRunsOfExperiment(ctx, d, xd.ID); err != nil || len(all) != 3 || all[2].ID != kruns[1].ID {
		t.Fatalf("eval runs of an experiment: %+v %v", all, err)
	}
	if got, err := s.GetEvalRun(ctx, d, tev.ID); err != nil || got.Type != core.TypeTraining || got.CaseRuns != nil {
		t.Fatalf("GetEvalRun: %+v %v", got, err)
	}
	if _, err := s.GetEvalRun(ctx, d, 999); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetEvalRun of a missing one: %v", err)
	}
	if runs, err := s.ListTrainingEvalRuns(ctx, d, 7, 0); err != nil || len(runs) != 1 || runs[0].ID != tev.ID {
		t.Fatalf("training eval runs of the model: %+v %v", runs, err)
	}
	if runs, err := s.ListTrainingEvalRuns(ctx, d, 8, 5); err != nil || runs == nil || len(runs) != 0 {
		t.Fatalf("training eval runs of another model: %+v %v", runs, err)
	}

	// The case runs of an eval run, in the order of the cases, with their metadata and trajectories.
	crs, err := s.ListCaseRuns(ctx, d, kruns[0].ID)
	must(t, err)
	if len(crs) != 2 || crs[0].ID != caseRuns[0][0].ID || crs[1].Verdict != core.VerdictFail || crs[0].Verdict != core.VerdictPass || crs[0].Score != nil || crs[0].CostUSD != 0.25 {
		t.Fatalf("case runs: %+v", crs)
	}
	m, ok := crs[0].Meta.(*core.KnowledgeCaseRunMeta)
	if !ok || m.Answer != "ans" || m.Steps != 4 || m.DatasetID == nil || *m.DatasetID != 40 || !reflect.DeepEqual(m.Problems, []string{"p"}) || !reflect.DeepEqual(m.PassedChecks, []string{"ok"}) ||
		m.Input.Query != "q?" || len(m.Trajectories) != 2 || m.Trajectories[0].Kind != "remember" || !reflect.DeepEqual(m.Trajectories[0].ArtifactIDs, []int64{5, 6}) ||
		m.Trajectories[0].Path != "/p/0.json" || m.Trajectories[1].Seq != 1 || m.Trajectories[1].Kind != "recall" || m.Trajectories[0].CreatedAt.IsZero() {
		t.Fatalf("knowledge case run meta: %#v", crs[0].Meta)
	}
	tcrs, err := s.ListCaseRuns(ctx, d, tev.ID)
	must(t, err)
	if len(tcrs) != 1 || tcrs[0].Score == nil || *tcrs[0].Score != 1 {
		t.Fatalf("training case runs: %+v", tcrs)
	}
	if m, ok := tcrs[0].Meta.(*core.TrainingCaseRunMeta); !ok || m.JudgeScore == nil || *m.JudgeScore != 5 || m.Response != "a" || m.Batch != 1 || m.JudgeReason != "r" || m.DurationMS != 12 {
		t.Fatalf("training case run meta: %#v", tcrs[0].Meta)
	}
	if c, err := s.GetCaseRun(ctx, d, caseRuns[1][1].ID); err != nil || c.Verdict != core.VerdictPass || knowledgeDataset(c) != 41 {
		t.Fatalf("GetCaseRun: %+v %v", c, err)
	}
	if _, err := s.GetCaseRun(ctx, d, 999); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetCaseRun of a missing one: %v", err)
	}

	// Trajectories by ID.
	ref, err := s.FindKnowledgeTrajectory(ctx, d, "tr-b1-1")
	if err != nil || ref.CaseRunID != caseRuns[1][1].ID || ref.Kind != "recall" || ref.AgentName != "gligabao-recall" {
		t.Fatalf("FindKnowledgeTrajectory: %+v %v", ref, err)
	}
	if _, err := s.FindKnowledgeTrajectory(ctx, d, "nope"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("FindKnowledgeTrajectory of a missing one: %v", err)
	}

	// The cases the experiment executed, and the history of one.
	cases, err := s.ListExperimentCases(ctx, d, xd.ID)
	must(t, err)
	if !reflect.DeepEqual(cases, []core.CaseRef{{Eval: "kn", Name: "c0"}, {Eval: "kn", Name: "c1"}, {Eval: "tr", Name: "t0"}}) {
		t.Fatalf("experiment cases: %+v", cases)
	}
	hist, err := s.ListCaseHistory(ctx, d, xd.ID, "kn", "c1")
	must(t, err)
	if len(hist) != 2 || hist[0].ExperimentRunID != xruns[0].ID || hist[1].ExperimentRunID != xruns[1].ID || hist[0].CaseRun.Verdict != core.VerdictFail ||
		hist[1].CaseRun.Verdict != core.VerdictPass {
		t.Fatalf("history: %+v", hist)
	}
	if hm, ok := hist[0].CaseRun.Meta.(*core.KnowledgeCaseRunMeta); !ok || hm.Steps != 4 || len(hm.Problems) != 1 || hm.Trajectories != nil {
		t.Fatalf("history meta: %#v", hist[0].CaseRun.Meta)
	}
	if h, err := s.ListCaseHistory(ctx, d, xd.ID, "kn", "nope"); err != nil || h == nil || len(h) != 0 {
		t.Fatalf("history of an unknown case: %v %v", h, err)
	}

	// A restart: what is running of the type is errored, with the experiment run it was in if all of it was of that type; the rest is
	// left alone.
	alone := &core.ExperimentRun{DefID: xd.ID, Status: core.StatusRunning, Timing: core.Timing{CreatedAt: start, StartedAt: &start}}
	must(t, s.CreateExperimentRun(ctx, d, alone))
	arun := &core.EvalRun{DefID: tr.ID, Type: core.TypeTraining, ExperimentRunID: &alone.ID, Status: core.StatusRunning, Counts: core.Counts{Total: 1, Pending: 1},
		Timing: core.Timing{CreatedAt: start, StartedAt: &start}}
	must(t, s.CreateEvalRun(ctx, d, arun))
	must(t, s.CreateEvalRunMeta(ctx, d, &core.TrainingEvalRunMeta{EvalRunID: arun.ID, TrainedModelID: 7}))
	run := &core.EvalRun{DefID: tr.ID, Type: core.TypeTraining, ExperimentRunID: &xruns[1].ID, Status: core.StatusRunning, Counts: core.Counts{Total: 2, Pending: 2},
		Timing: core.Timing{CreatedAt: start, StartedAt: &start}}
	must(t, s.CreateEvalRun(ctx, d, run))
	must(t, s.CreateEvalRunMeta(ctx, d, &core.TrainingEvalRunMeta{EvalRunID: run.ID, TrainedModelID: 7}))
	krun := &core.EvalRun{DefID: kn.ID, Type: core.TypeKnowledge, Status: core.StatusRunning, Timing: core.Timing{CreatedAt: start, StartedAt: &start}}
	must(t, s.CreateEvalRun(ctx, d, krun))
	// xruns[1] has a completed knowledge eval run, so it is not ended by the failure of a training one.
	n, err := s.FailRunning(ctx, d, core.TypeTraining, "restart", time.Now())
	must(t, err)
	if got, err := s.GetExperimentRun(ctx, d, alone.ID); n != 2 || err != nil || got.Status != core.StatusErrored || got.Error != "restart" || got.FinishedAt == nil {
		t.Fatalf("FailRunning, the experiment run of a training eval alone: %d %+v %v", n, got, err)
	}
	if got, err := s.GetExperimentRun(ctx, d, xruns[1].ID); err != nil || got.Status != core.StatusRunning {
		t.Fatalf("FailRunning, an experiment run with an eval of another type: %+v %v", got, err)
	}
	if got, err := s.GetEvalRun(ctx, d, run.ID); n != 2 || err != nil || got.Status != core.StatusErrored || got.Error != "restart" || got.FinishedAt == nil {
		t.Fatalf("FailRunning: %d %+v %v", n, got, err)
	}
	if got, err := s.GetEvalRun(ctx, d, krun.ID); err != nil || got.Status != core.StatusRunning {
		t.Fatalf("a running eval run of another type: %+v %v", got, err)
	}
	if n, err := s.FailRunning(ctx, d, core.TypeTraining, "again", time.Now()); err != nil || n != 0 {
		t.Fatalf("FailRunning again: %d %v", n, err)
	}
}

func ptr[T any](v T) *T { return &v }

func knowledgeDataset(c *core.CaseRun) int64 {
	if m, ok := c.Meta.(*core.KnowledgeCaseRunMeta); ok && m.DatasetID != nil {
		return *m.DatasetID
	}
	return 0
}
