package runner_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/evals"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"

	_ "modernc.org/sqlite"
)

func newRunner(t *testing.T) *runner.Runner {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := evals.Open(config.DBBackendSQLite, d); err != nil {
		t.Fatal(err)
	}
	store, err := evals.StoreFor(config.DBBackendSQLite)
	if err != nil {
		t.Fatal(err)
	}
	return runner.New(d, store)
}

func trainingSpec(names ...string) runner.EvalSpec {
	spec := runner.EvalSpec{Name: "tr", Type: core.TypeTraining, Description: "d"}
	for _, n := range names {
		spec.Cases = append(spec.Cases, runner.CaseSpec{Name: n, Meta: &core.TrainingCaseDefMeta{
			TrainingInput: core.TrainingInput{Instruction: "i " + n}, TrainingExpected: core.TrainingExpected{Output: "o " + n}}})
	}
	return spec
}

func caseNames(def *core.EvalDef) (names []string) {
	for i, c := range def.Cases {
		if c.Position != i {
			names = append(names, "BAD POSITION")
		}
		names = append(names, c.Name)
	}
	return names
}

func count(t *testing.T, r *runner.Runner, query string, args ...any) (n int) {
	t.Helper()
	if err := r.DB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestSyncEval(t *testing.T) {
	ctx := context.Background()
	r := newRunner(t)

	def, err := r.SyncEval(ctx, trainingSpec("a", "b", "c"))
	if err != nil {
		t.Fatal(err)
	}
	if def.ID == 0 || def.Description != "d" || !reflect.DeepEqual(caseNames(def), []string{"a", "b", "c"}) {
		t.Fatalf("created: %+v %v", def, caseNames(def))
	}
	ids := map[string]core.CaseDefID{}
	for _, c := range def.Cases {
		ids[c.Name] = c.ID
	}

	// Syncing the same eval again changes nothing.
	again, err := r.SyncEval(ctx, trainingSpec("a", "b", "c"))
	if err != nil || again.ID != def.ID || !reflect.DeepEqual(caseNames(again), []string{"a", "b", "c"}) {
		t.Fatalf("again: %+v %v", again, err)
	}

	// Reordered, with a changed case, a new case and one dropped.
	spec := trainingSpec("c", "a", "d")
	spec.Cases[1].Meta = &core.TrainingCaseDefMeta{TrainingInput: core.TrainingInput{Instruction: "changed"}, TrainingExpected: core.TrainingExpected{Output: "o a"}}
	spec.Description = "new description"
	got, err := r.SyncEval(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(caseNames(got), []string{"c", "a", "d"}) || got.Description != "new description" {
		t.Fatalf("changed: %v %+v", caseNames(got), got)
	}
	if got.Cases[0].ID != ids["c"] || got.Cases[1].ID != ids["a"] {
		t.Errorf("the case defs that exist were not kept: %+v", got.Cases)
	}
	if m := got.Cases[1].Meta.(*core.TrainingCaseDefMeta); m.Instruction != "changed" || m.CaseDefID != ids["a"] {
		t.Errorf("the metadata was not rewritten: %+v", m)
	}
	// What is stored: the dropped case is kept, after the others.
	stored, err := r.Store.GetEvalDefByName(ctx, r.DB, "tr")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(caseNames(stored), []string{"c", "a", "d", "b"}) {
		t.Errorf("stored cases: %v", caseNames(stored))
	}

	// A different type is refused, and so is a case of the wrong type.
	if _, err := r.SyncEval(ctx, runner.EvalSpec{Name: "tr", Type: core.TypeKnowledge}); err == nil {
		t.Error("accepted an eval of another type")
	}
	bad := runner.EvalSpec{Name: "kn", Type: core.TypeKnowledge, Cases: []runner.CaseSpec{{Name: "x", Meta: &core.TrainingCaseDefMeta{}}}}
	if _, err := r.SyncEval(ctx, bad); err == nil {
		t.Error("accepted a case whose metadata is of another type")
	}
}

func TestEnsureExperiment(t *testing.T) {
	ctx := context.Background()
	r := newRunner(t)
	a, _ := r.SyncEval(ctx, trainingSpec("a"))
	spec := trainingSpec("b")
	spec.Name = "tr2"
	b, _ := r.SyncEval(ctx, spec)

	x, err := r.EnsureExperiment(ctx, "x", "desc", []core.EvalDefID{a.ID})
	if err != nil || x.ID == 0 || !reflect.DeepEqual(x.EvalDefIDs, []core.EvalDefID{a.ID}) {
		t.Fatalf("EnsureExperiment: %+v %v", x, err)
	}
	x2, err := r.EnsureExperiment(ctx, "x", "", []core.EvalDefID{b.ID, a.ID})
	if err != nil || x2.ID != x.ID || !reflect.DeepEqual(x2.EvalDefIDs, []core.EvalDefID{a.ID, b.ID}) {
		t.Fatalf("EnsureExperiment again: %+v %v", x2, err)
	}
	if _, err := r.EnsureExperiment(ctx, "", "", nil); err == nil {
		t.Error("accepted an experiment without a name")
	}
}

// fakeExec records a case run for each case: the first passes, the second errors, the third is judged and fails.
type fakeExec struct{ err error }

func (f fakeExec) Execute(ctx context.Context, rec *runner.EvalRecorder, def *core.EvalDef) error {
	meta := rec.Run().Meta.(*core.TrainingEvalRunMeta)
	cr, err := rec.StartCase(ctx, def.Cases[0])
	if err != nil {
		return err
	}
	score, js := 1.0, 5
	cr.Status, cr.Verdict, cr.Score, cr.CostUSD = core.StatusCompleted, core.VerdictPass, &score, 0.5
	meta.NumJudged, meta.NumCorrect, meta.ScoreSum = 1, 1, 5
	if err := rec.FinishCase(ctx, cr, &core.TrainingCaseRunMeta{Instruction: "i", JudgeScore: &js}); err != nil {
		return err
	}
	low, lowScore := 0.2, 1
	meta.NumJudged, meta.NumIncorrect, meta.ScoreSum = 3, 2, 6
	err = rec.RecordCases(ctx, []runner.CaseRecord{
		{Run: &core.CaseRun{DefID: def.Cases[1].ID, Type: def.Type, Status: core.StatusErrored, Error: "model: down"}, Meta: &core.TrainingCaseRunMeta{Instruction: "i"}},
		{Run: &core.CaseRun{DefID: def.Cases[2].ID, Type: def.Type, Status: core.StatusCompleted, Verdict: core.VerdictFail, Score: &low}, Meta: &core.TrainingCaseRunMeta{Instruction: "i", JudgeScore: &lowScore}},
	})
	if err != nil {
		return err
	}
	return f.err
}

func TestRunEval(t *testing.T) {
	ctx := context.Background()
	r := newRunner(t)
	def, err := r.SyncEval(ctx, trainingSpec("a", "b", "c", "d"))
	if err != nil {
		t.Fatal(err)
	}
	x, err := r.EnsureExperiment(ctx, "x", "", []core.EvalDefID{def.ID})
	if err != nil {
		t.Fatal(err)
	}
	xr, err := r.StartExperimentRun(ctx, x, core.Params{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}

	opts := runner.EvalOptions{ExperimentRunID: &xr.Run().ID, Params: core.Params{"cases": []string{"a"}}, Meta: &core.TrainingEvalRunMeta{TrainedModelID: 7}}
	run, err := r.RunEval(ctx, def, fakeExec{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := core.Counts{Total: 4, Pending: 1, Completed: 2, Errored: 1, Passed: 1, Failed: 1}
	if run.Status != core.StatusCompleted || run.Counts != want || run.CostUSD != 0.5 || run.FinishedAt == nil || run.StartedAt == nil {
		t.Errorf("eval run: %+v", run)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM gb_eval_runs e JOIN gb_eval_runs_meta_training m ON m.eval_run_id = e.id
		WHERE e.id = ? AND e.status = 'completed' AND e.counts_pending = 1 AND e.counts_passed = 1 AND e.counts_failed = 1 AND e.counts_errored = 1
		AND e.experiment_run_id = ? AND m.trained_model_id = 7 AND m.num_judged = 3 AND m.num_correct = 1 AND m.num_incorrect = 2`, run.ID, xr.Run().ID); n != 1 {
		t.Errorf("the stored eval run: %d", n)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM gb_case_runs c JOIN gb_case_runs_meta_training m ON m.case_run_id = c.id WHERE c.eval_run_id = ?`, run.ID); n != 3 {
		t.Errorf("case runs with their metadata: %d", n)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM gb_case_runs WHERE eval_run_id = ? AND status = 'errored' AND error = 'model: down' AND finished_at IS NOT NULL`, run.ID); n != 1 {
		t.Errorf("the errored case run: %d", n)
	}

	if err := xr.Add(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := xr.Finish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM gb_experiment_runs WHERE id = ? AND status = 'completed' AND counts_total = 4 AND counts_passed = 1 AND cost_usd = 0.5 AND finished_at IS NOT NULL`, xr.Run().ID); n != 1 {
		t.Errorf("the stored experiment run: %d", n)
	}

	// An executor that fails ends the run errored, one that is cancelled ends it cancelled; what was recorded stays.
	opts.ExperimentRunID, opts.Meta = nil, &core.TrainingEvalRunMeta{TrainedModelID: 7}
	run, err = r.RunEval(ctx, def, fakeExec{err: errors.New("judge is down")}, opts)
	if err == nil || run.Status != core.StatusErrored || run.Error != "judge is down" || run.Counts.Completed != 2 {
		t.Errorf("errored run: %+v %v", run, err)
	}
	opts.Meta = &core.TrainingEvalRunMeta{TrainedModelID: 7}
	run, err = r.RunEval(ctx, def, fakeExec{err: context.Canceled}, opts)
	if !errors.Is(err, context.Canceled) || run.Status != core.StatusCancelled {
		t.Errorf("cancelled run: %+v %v", run, err)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM gb_eval_runs WHERE experiment_run_id IS NULL AND status IN ('errored', 'cancelled')`); n != 2 {
		t.Errorf("stored runs that ended badly: %d", n)
	}
}
