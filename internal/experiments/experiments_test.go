package experiments_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/experiments"
)

func TestBackends(t *testing.T) { dbtest.Run(t, scenario) }

// traj builds a finished trajectory of the given operation ("remember" or "recall") with one LLM step.
func traj(op string, cost float64) *atif.Trajectory {
	b := atif.NewBuilder(atif.Agent{Name: "gligabao-" + op, Version: "0.1"})
	b.User("input")
	b.Agent(atif.Step{ModelName: "m", Message: "out", Metrics: &atif.Metrics{PromptTokens: 10, CompletionTokens: 2, CostUSD: cost}})
	return b.Build()
}

func scenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	if _, err := experiments.Start(ctx, d, experiments.Options{}); err == nil {
		t.Fatal("Start without a name succeeded")
	}
	if _, err := experiments.Get(ctx, d, "base"); !errors.Is(err, experiments.ErrNotFound) {
		t.Fatalf("Get missing: %v", err)
	}

	// Run 1: one passing case (2 trajectories), one failing check, one error.
	tr, err := experiments.Start(ctx, d, experiments.Options{
		Name: "base", Description: "baseline", Config: map[string]any{"model": "m"}, Params: map[string]any{"only": []string{"a"}},
	})
	check(err)
	if tr.Experiment.ID == 0 || tr.Experiment.Description != "baseline" || tr.Experiment.Config["model"] != "m" ||
		tr.Run.Status != experiments.StatusRunning || tr.Run.FinishedAt != nil || tr.Run.StartedAt.IsZero() {
		t.Fatalf("started: %+v / %+v", tr.Experiment, tr.Run)
	}

	rem, rec := traj("remember", 0.01), traj("recall", 0.02)
	started := time.Now().Add(-time.Minute)
	pass, err := tr.Record(ctx, experiments.CaseOutcome{
		Eval: "basic", Name: "a", Answer: "yes", StartedAt: started,
		Trajectories: []*atif.Trajectory{rem, rec}, TrajectoryDir: "/tmp/atif",
	})
	check(err)
	if pass.ID == 0 || pass.Status != experiments.StatusPassed || pass.Steps != 4 || len(pass.Trajectories) != 2 ||
		pass.Trajectories[0].Seq != 1 || pass.Trajectories[1].Seq != 2 || pass.Trajectories[1].ID == 0 {
		t.Fatalf("recorded: %+v", pass)
	}
	_, err = tr.Record(ctx, experiments.CaseOutcome{Eval: "basic", Name: "b", Problems: []string{"x: no edge stored"}, Trajectories: []*atif.Trajectory{traj("remember", 0.5)}})
	check(err)
	_, err = tr.Record(ctx, experiments.CaseOutcome{Eval: "news", Name: "c", Err: errors.New("boom")})
	check(err)
	if _, err := tr.Record(ctx, experiments.CaseOutcome{Name: "x"}); err == nil {
		t.Fatal("Record without an eval succeeded")
	}
	if _, err := tr.Record(ctx, experiments.CaseOutcome{Eval: "s", Name: "x", Trajectories: []*atif.Trajectory{{TrajectoryID: "t", Agent: atif.Agent{Name: "other"}}}}); err == nil {
		t.Fatal("Record with an unknown agent succeeded")
	}

	run, err := tr.Finish(ctx)
	check(err)
	if run.Status != experiments.StatusFailed || run.TotalCases != 3 || run.PassedCases != 1 || run.FailedCases != 1 || run.ErrorCases != 1 || run.JudgedCases != 0 ||
		run.FinishedAt == nil || run.CostUSD < 0.529 || run.CostUSD > 0.531 {
		t.Fatalf("finished: %+v", run)
	}
	if _, err := tr.Record(ctx, experiments.CaseOutcome{Eval: "basic", Name: "late"}); !errors.Is(err, experiments.ErrRunNotRunning) {
		t.Fatalf("Record after Finish: %v", err)
	}
	if again, err := tr.Finish(ctx); err != nil || again.FinishedAt == nil || !again.FinishedAt.Equal(*run.FinishedAt) {
		t.Fatalf("second Finish: %+v, %v", again, err)
	}

	// The full run, as stored.
	full, err := experiments.GetRun(ctx, d, run.ID)
	check(err)
	if len(full.CaseResults) != 3 || full.Params["only"] == nil {
		t.Fatalf("GetRun: %+v", full)
	}
	a, b, c := full.CaseResults[0], full.CaseResults[1], full.CaseResults[2]
	if a.Eval != "basic" || a.Name != "a" || a.Answer != "yes" || a.Status != experiments.StatusPassed || len(a.Problems) != 0 ||
		a.StartedAt.After(a.FinishedAt) || len(a.Trajectories) != 2 {
		t.Fatalf("case a: %+v", a)
	}
	if got := a.Trajectories[0]; got.TrajectoryID != rem.TrajectoryID || got.Kind != "remember" || got.Steps != 2 ||
		got.Path != "/tmp/atif/"+rem.TrajectoryID+".json" || got.CaseResultID != a.ID || got.CreatedAt.IsZero() {
		t.Fatalf("trajectory: %+v", got)
	}
	if got := a.Trajectories[1]; got.TrajectoryID != rec.TrajectoryID || got.Kind != "recall" || got.CostUSD != 0.02 {
		t.Fatalf("trajectory: %+v", got)
	}
	if b.Status != experiments.StatusFailed || !reflect.DeepEqual(b.Problems, []string{"x: no edge stored"}) || b.Trajectories[0].Path != "" {
		t.Fatalf("case b: %+v", b)
	}
	if c.Status != experiments.StatusErrored || c.Error != "boom" || len(c.Trajectories) != 0 {
		t.Fatalf("case c: %+v", c)
	}

	// The experiment knows which cases ran.
	exp, err := experiments.Get(ctx, d, "base")
	check(err)
	want := []experiments.CaseRef{{Eval: "basic", Name: "a"}, {Eval: "basic", Name: "b"}, {Eval: "news", Name: "c"}}
	if !reflect.DeepEqual(exp.Cases, want) {
		t.Fatalf("experiment cases: %+v", exp.Cases)
	}

	// Run 2 of the same experiment: it is reused, the cases are not duplicated, and a passing run is passed.
	tr2, err := experiments.Start(ctx, d, experiments.Options{Name: "base", Description: "ignored"})
	check(err)
	if tr2.Experiment.ID != exp.ID || tr2.Experiment.Description != "baseline" || tr2.Run.ID == run.ID {
		t.Fatalf("second run: %+v / %+v", tr2.Experiment, tr2.Run)
	}
	_, err = tr2.Record(ctx, experiments.CaseOutcome{Eval: "basic", Name: "a"})
	check(err)
	run2, err := tr2.Finish(ctx)
	check(err)
	if run2.Status != experiments.StatusPassed || run2.TotalCases != 1 || run2.FailedCases != 0 {
		t.Fatalf("run2: %+v", run2)
	}
	if exp, err = experiments.Get(ctx, d, "base"); err != nil || len(exp.Cases) != 3 {
		t.Fatalf("experiment cases after run 2: %+v, %v", exp, err)
	}
	if runs, err := experiments.Runs(ctx, d, exp.ID); err != nil || len(runs) != 2 || runs[0].ID != run.ID || len(runs[0].CaseResults) != 0 {
		t.Fatalf("Runs: %+v, %v", runs, err)
	}

	// Another experiment, and lookups.
	_, err = experiments.Start(ctx, d, experiments.Options{Name: "other"})
	check(err)
	if list, err := experiments.List(ctx, d); err != nil || len(list) != 2 || list[0].Name != "base" {
		t.Fatalf("List: %+v, %v", list, err)
	}
	ref, err := experiments.FindTrajectory(ctx, d, rec.TrajectoryID)
	check(err)
	if ref.CaseResultID != a.ID || ref.Seq != 2 {
		t.Fatalf("FindTrajectory: %+v", ref)
	}
	if _, err := experiments.FindTrajectory(ctx, d, "nope"); !errors.Is(err, experiments.ErrNotFound) {
		t.Fatalf("FindTrajectory missing: %v", err)
	}
	if _, err := experiments.GetRun(ctx, d, 99999); !errors.Is(err, experiments.ErrNotFound) {
		t.Fatalf("GetRun missing: %v", err)
	}
}
