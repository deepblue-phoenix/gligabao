package sqlite_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db/sqlite"
)

// TestGetAndUpdate stores defs through the Store, reads them back, rewrites them, and runs a case run through its life.
func TestGetAndUpdate(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	s := sqlite.Store

	if _, err := s.GetEvalDefByName(ctx, d, "nope"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetEvalDefByName of a missing eval: %v", err)
	}
	if _, err := s.GetExperimentDefByName(ctx, d, "nope"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetExperimentDefByName of a missing experiment: %v", err)
	}

	ev := &core.EvalDef{Name: "kn", Type: core.TypeKnowledge, Tags: []string{"a"}, Params: core.Params{"judge": "x"}}
	if err := s.CreateEvalDef(ctx, d, ev); err != nil {
		t.Fatal(err)
	}
	var metas []*core.KnowledgeCaseDefMeta
	for i, name := range []string{"c0", "c1"} {
		c := &core.CaseDef{EvalDefID: ev.ID, Type: core.TypeKnowledge, Name: name, Position: i, Tags: []string{"t"}}
		if err := s.CreateCaseDef(ctx, d, c); err != nil {
			t.Fatal(err)
		}
		m := &core.KnowledgeCaseDefMeta{CaseDefID: c.ID}
		m.Remember = []core.KnowledgeArtifact{{Text: "Alice works at Acme.", WebSearch: true}}
		m.Query = "Where does Alice work?"
		m.ExpectContains = []string{"Acme"}
		m.ExpectEdges = []core.KnowledgeExpectEdge{{Source: "Alice", Target: "Acme", Ended: true}}
		m.ExpectSeries = []core.KnowledgeExpectSeries{{Key: "k", Points: 3}}
		m.Models = map[string]string{"extract_graph": "m"}
		m.ChunkSize = 100
		if err := s.CreateCaseDefMeta(ctx, d, m); err != nil {
			t.Fatal(err)
		}
		metas = append(metas, m)
	}

	got, err := s.GetEvalDefByName(ctx, d, "kn")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ev.ID || got.Type != core.TypeKnowledge || !reflect.DeepEqual(got.Tags, []string{"a"}) || got.Params["judge"] != "x" || got.CreatedAt.IsZero() {
		t.Errorf("eval def: %+v", got)
	}
	if len(got.Cases) != 2 || got.Cases[0].Name != "c0" || got.Cases[1].Name != "c1" || got.Cases[1].Position != 1 {
		t.Fatalf("cases: %+v", got.Cases)
	}
	if m, ok := got.Cases[0].Meta.(*core.KnowledgeCaseDefMeta); !ok || !reflect.DeepEqual(m, metas[0]) {
		t.Errorf("case meta: %#v, want %#v", got.Cases[0].Meta, metas[0])
	}

	// A training eval, with its case.
	tr := &core.EvalDef{Name: "tr", Type: core.TypeTraining}
	if err := s.CreateEvalDef(ctx, d, tr); err != nil {
		t.Fatal(err)
	}
	tc := &core.CaseDef{EvalDefID: tr.ID, Type: core.TypeTraining, Name: "q"}
	if err := s.CreateCaseDef(ctx, d, tc); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateCaseDefMeta(ctx, d, core.TrainingCaseDefMeta{CaseDefID: tc.ID, TrainingInput: core.TrainingInput{Instruction: "i", Input: "x"}, TrainingExpected: core.TrainingExpected{Output: "o"}}); err != nil {
		t.Fatal(err)
	}
	gt, err := s.GetEvalDefByName(ctx, d, "tr")
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := gt.Cases[0].Meta.(*core.TrainingCaseDefMeta); !ok || m.Instruction != "i" || m.Input != "x" || m.Output != "o" {
		t.Errorf("training case meta: %#v", gt.Cases[0].Meta)
	}

	// Updates.
	got.Description, got.Tags, got.Params = "d", []string{"b", "c"}, core.Params{"n": 2.0}
	if err := s.UpdateEvalDef(ctx, d, got); err != nil {
		t.Fatal(err)
	}
	metas[1].Query, metas[1].ExpectContains, metas[1].ChunkSize = "other?", nil, 0
	if err := s.UpdateCaseDefMeta(ctx, d, metas[1]); err != nil {
		t.Fatal(err)
	}
	c0 := got.Cases[0]
	c0.Position, c0.Description = 5, "moved"
	if err := s.UpdateCaseDef(ctx, d, &c0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateCaseDef(ctx, d, &core.CaseDef{ID: 9999}); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("UpdateCaseDef of a missing case def: %v", err)
	}
	if err := s.UpdateCaseDefMeta(ctx, d, &core.TrainingCaseDefMeta{CaseDefID: 9999}); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("UpdateCaseDefMeta of a missing case def: %v", err)
	}
	again, err := s.GetEvalDefByName(ctx, d, "kn")
	if err != nil {
		t.Fatal(err)
	}
	if again.Description != "d" || !reflect.DeepEqual(again.Tags, []string{"b", "c"}) || again.Params["n"] != 2.0 || again.Name != "kn" || !again.UpdatedAt.After(time.Time{}) {
		t.Errorf("updated eval def: %+v", again)
	}
	// c1 (position 1) now comes first; c0 is at 5.
	if again.Cases[0].Name != "c1" || again.Cases[1].Name != "c0" || again.Cases[1].Description != "moved" {
		t.Errorf("updated cases: %+v", again.Cases)
	}
	if m := again.Cases[0].Meta.(*core.KnowledgeCaseDefMeta); m.Query != "other?" || len(m.ExpectContains) != 0 || m.ChunkSize != 0 {
		t.Errorf("updated meta: %+v", m)
	}

	// Experiment defs.
	ex := &core.ExperimentDef{Name: "x", Params: core.Params{"k": "v"}}
	if err := s.CreateExperimentDef(ctx, d, ex); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateExperimentDefEval(ctx, d, ex.ID, tr.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateExperimentDefEval(ctx, d, ex.ID, ev.ID, 1); err != nil {
		t.Fatal(err)
	}
	gx, err := s.GetExperimentDefByName(ctx, d, "x")
	if err != nil || gx.ID != ex.ID || !reflect.DeepEqual(gx.EvalDefIDs, []core.EvalDefID{tr.ID, ev.ID}) || gx.Params["k"] != "v" {
		t.Fatalf("experiment def: %+v %v", gx, err)
	}

	// A run through its life: the case run starts, finishes with its metadata, the eval run follows.
	xr := &core.ExperimentRun{DefID: ex.ID}
	if err := s.CreateExperimentRun(ctx, d, xr); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	xr.Status, xr.StartedAt = core.StatusRunning, &t0
	if err := s.UpdateExperimentRun(ctx, d, xr); err != nil {
		t.Fatal(err)
	}
	er := &core.EvalRun{DefID: ev.ID, Type: core.TypeKnowledge, ExperimentRunID: &xr.ID, Status: core.StatusRunning, Counts: core.Counts{Total: 2, Pending: 2}, Timing: core.Timing{StartedAt: &t0}}
	if err := s.CreateEvalRun(ctx, d, er); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateEvalRunMeta(ctx, d, &core.KnowledgeEvalRunMeta{EvalRunID: er.ID}); err != nil {
		t.Fatal(err)
	}
	cr := &core.CaseRun{DefID: again.Cases[0].ID, EvalRunID: er.ID, Type: core.TypeKnowledge, Status: core.StatusRunning, Timing: core.Timing{StartedAt: &t0}}
	if err := s.CreateCaseRun(ctx, d, cr); err != nil {
		t.Fatal(err)
	}
	score, t1 := 1.0, time.Now()
	cr.Status, cr.Verdict, cr.Score, cr.CostUSD, cr.FinishedAt = core.StatusCompleted, core.VerdictPass, &score, 0.25, &t1
	if err := s.UpdateCaseRun(ctx, d, cr); err != nil {
		t.Fatal(err)
	}
	meta := &core.KnowledgeCaseRunMeta{CaseRunID: cr.ID, Answer: "Acme", Steps: 3, Trajectories: []core.KnowledgeTrajectory{
		{Seq: 0, Kind: "remember", TrajectoryID: "t1", ArtifactIDs: []int64{4}}, {Seq: 1, Kind: "recall", TrajectoryID: "t2"}}}
	if err := s.CreateCaseRunMeta(ctx, d, meta); err != nil {
		t.Fatal(err)
	}
	er.Counts = core.Counts{Total: 2, Pending: 1, Completed: 1, Passed: 1}
	er.CostUSD, er.Status, er.FinishedAt = 0.25, core.StatusCompleted, &t1
	if err := s.UpdateEvalRun(ctx, d, er); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateEvalRunMeta(ctx, d, &core.KnowledgeEvalRunMeta{EvalRunID: er.ID, Steps: 3, RememberCalls: 1, ChecksPassed: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateEvalRunMeta(ctx, d, &core.KnowledgeEvalRunMeta{EvalRunID: 9999}); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("UpdateEvalRunMeta of a missing run: %v", err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM gb_case_runs WHERE id = ? AND status = 'completed' AND verdict = 'pass' AND score = 1 AND cost_usd = 0.25 AND finished_at IS NOT NULL`, cr.ID); n != 1 {
		t.Errorf("case run: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM gb_case_runs_meta_knowledge_trajectories WHERE case_run_id = ?`, cr.ID); n != 2 {
		t.Errorf("trajectories: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM gb_eval_runs e JOIN gb_eval_runs_meta_knowledge m ON m.eval_run_id = e.id
		WHERE e.id = ? AND e.status = 'completed' AND e.counts_pending = 1 AND e.counts_passed = 1 AND e.cost_usd = 0.25 AND m.steps = 3 AND m.checks_passed = 2`, er.ID); n != 1 {
		t.Errorf("eval run: %d", n)
	}
	xr.Status, xr.Counts, xr.FinishedAt = core.StatusCompleted, er.Counts, &t1
	if err := s.UpdateExperimentRun(ctx, d, xr); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM gb_experiment_runs WHERE id = ? AND status = 'completed' AND counts_passed = 1 AND started_at IS NOT NULL`, xr.ID); n != 1 {
		t.Errorf("experiment run: %d", n)
	}

	// A training case run and eval run meta.
	ter := &core.EvalRun{DefID: tr.ID, Type: core.TypeTraining, Status: core.StatusRunning}
	if err := s.CreateEvalRun(ctx, d, ter); err != nil {
		t.Fatal(err)
	}
	tm := &core.TrainingEvalRunMeta{EvalRunID: ter.ID, TrainedModelID: 3}
	if err := s.CreateEvalRunMeta(ctx, d, tm); err != nil {
		t.Fatal(err)
	}
	tm.NumAnswered, tm.NumJudged, tm.NumCorrect, tm.ScoreSum, tm.JudgeModel = 1, 1, 1, 5, "j"
	if err := s.UpdateEvalRunMeta(ctx, d, tm); err != nil {
		t.Fatal(err)
	}
	tcr := &core.CaseRun{DefID: tc.ID, EvalRunID: ter.ID, Type: core.TypeTraining, Status: core.StatusCompleted}
	if err := s.CreateCaseRun(ctx, d, tcr); err != nil {
		t.Fatal(err)
	}
	js := 5
	if err := s.CreateCaseRunMeta(ctx, d, &core.TrainingCaseRunMeta{CaseRunID: tcr.ID, Instruction: "i", JudgeScore: &js}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM gb_eval_runs_meta_training WHERE eval_run_id = ? AND judge_model = 'j' AND trained_model_id = 3 AND score_sum = 5`, ter.ID); n != 1 {
		t.Errorf("training eval run meta: %d", n)
	}
	if err := s.CreateCaseRunMeta(ctx, d, struct{ core.CaseRunMeta }{}); err == nil {
		t.Error("accepted metadata of an unknown type")
	}
}
