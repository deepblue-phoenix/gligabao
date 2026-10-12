package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/experiments"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
)

// scriptedLLM answers by what the prompt asks for: the recall prompt gets an answer, anything else an extraction.
type scriptedLLM struct{ answer string }

func (l *scriptedLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	out := `{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`
	if len(req.Messages) > 0 && strings.Contains(req.Messages[len(req.Messages)-1].Content, "<graph>") {
		out = `{"answer":"` + l.answer + `","entities":[1],"edges":[]}`
	}
	return &ai.Response{Text: out, StopReason: "end_turn", Provider: ai.ProviderAnthropic, Model: "m", InputTokens: 10, OutputTokens: 5, CostUSD: 0.01}, nil
}

// TestRunCasesTracksExperiment runs two cases (one passing, one failing its expected answer) and checks what the
// experiment run holds: the evals with their suite, the results and the trajectory IDs.
func TestRunCasesTracksExperiment(t *testing.T) {
	dir := t.TempDir()
	atifDir = filepath.Join(dir, "atif")
	memory.TrajectoryDir = atifDir
	t.Cleanup(func() { atifDir = "" })

	cases := []Case{
		{Suite: "s1", Name: "good", Remember: []Artifact{{Text: "Alice is here."}}, Query: "Who?", ExpectContains: []string{"alice"}},
		{Suite: "s2", Name: "bad", Remember: []Artifact{{Text: "Alice is here."}, {Text: "Alice again."}}, Query: "Who?", ExpectContains: []string{"Bob"}},
	}

	trackDB, err := db.Open(db.Config{SQLitePath: filepath.Join(dir, "eval.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer trackDB.Close()
	ctx := context.Background()
	tracker, err := experiments.Start(ctx, trackDB.Relational, experiments.Options{Name: "exp"})
	if err != nil {
		t.Fatal(err)
	}
	if failed := runCases(cases, &scriptedLLM{answer: "Alice."}, nil, false, trackDB, tracker); failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}
	run, err := tracker.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != experiments.StatusFailed || run.TotalEvals != 2 || run.PassedEvals != 1 || run.FailedEvals != 1 {
		t.Fatalf("run: %+v", run)
	}

	full, err := experiments.GetRun(ctx, trackDB.Relational, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	good, bad := full.Evals[0], full.Evals[1]
	if good.Suite != "s1" || good.Name != "good" || good.Status != experiments.StatusPassed || len(good.Trajectories) != 2 {
		t.Fatalf("good: %+v", good)
	}
	if bad.Suite != "s2" || bad.Status != experiments.StatusFailed || len(bad.Problems) != 1 || len(bad.Trajectories) != 3 {
		t.Fatalf("bad: %+v", bad)
	}
	if len(good.PassedChecks) != 1 || len(bad.PassedChecks) != 0 {
		t.Fatalf("passed checks: good %v, bad %v", good.PassedChecks, bad.PassedChecks)
	}
	if a := good.Trajectories[0].Artifacts; len(a) != 1 || a[0].Text != "Alice is here." || a[0].ID == 0 {
		t.Fatalf("remember artifacts: %+v", a)
	}
	ds, err := experiments.RunEvalDataset(ctx, trackDB.Relational, good.ID)
	if err != nil || good.DatasetID == nil || ds.ID != *good.DatasetID || ds.Name != datasetName(cases[0], run.ID) {
		t.Fatalf("dataset: %+v, %v, dataset_id=%v", ds, err, good.DatasetID)
	}
	if g, err := memory.ExportDatasetGraph(ctx, trackDB.Relational, ds); err != nil || len(g.Entities) == 0 {
		t.Fatalf("graph: %+v, %v", g, err)
	}
	for _, e := range full.Evals {
		for _, tr := range e.Trajectories {
			if _, err := os.Stat(tr.Path); err != nil {
				t.Fatalf("trajectory %s: %v", tr.TrajectoryID, err)
			}
		}
	}
	if k := good.Trajectories; k[0].Kind != "remember" || k[1].Kind != "recall" {
		t.Fatalf("kinds: %+v", k)
	}
	exp, err := experiments.Get(ctx, trackDB.Relational, "exp")
	if err != nil || len(exp.Evals) != 2 || exp.Evals[0].Suite != "s1" || exp.Evals[1].Suite != "s2" {
		t.Fatalf("experiment: %+v, %v", exp, err)
	}
}

func TestLoadCasesSetsSuite(t *testing.T) {
	cases, err := loadCases(nil, "")
	if err != nil || len(cases) == 0 {
		t.Fatalf("%v, %v", cases, err)
	}
	for _, c := range cases {
		if c.Suite == "" {
			t.Fatalf("case %q has no suite", c.Name)
		}
	}
}

// TestRunsAreIsolatedAndKept runs the same experiment twice against the same eval database: the second run does not
// wipe the first one's datasets, and each run works in datasets of its own.
func TestRunsAreIsolatedAndKept(t *testing.T) {
	dir := t.TempDir()
	atifDir = filepath.Join(dir, "atif")
	memory.TrajectoryDir = atifDir
	t.Cleanup(func() { atifDir = "" })
	database, err := db.Open(db.Config{SQLitePath: filepath.Join(dir, "eval.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	cases := []Case{{Suite: "s", Name: "c", Remember: []Artifact{{Text: "Alice is here."}}, Query: "Who?", ExpectContains: []string{"alice"}}}

	var runs []int64
	for i := 0; i < 2; i++ {
		tr, err := experiments.Start(ctx, database.Relational, experiments.Options{Name: "exp"})
		if err != nil {
			t.Fatal(err)
		}
		if failed := runCases(cases, &scriptedLLM{answer: "Alice."}, nil, false, database, tr); failed != 0 {
			t.Fatalf("run %d failed", i)
		}
		if _, err := tr.Finish(ctx); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, tr.Run.ID)
	}
	for _, id := range runs {
		name := datasetName(cases[0], id)
		if _, err := relational.DatasetID(ctx, database.Relational, evalUser, name); err != nil {
			t.Fatalf("dataset %q of run %d: %v", name, id, err)
		}
	}
	if runs[0] == runs[1] || datasetName(cases[0], runs[0]) == datasetName(cases[0], runs[1]) {
		t.Fatalf("runs share datasets: %v", runs)
	}
	if list, err := experiments.Runs(ctx, database.Relational, 1); err != nil || len(list) != 2 {
		t.Fatalf("runs: %v, %v", list, err)
	}
}

func TestDatasetName(t *testing.T) {
	for name, want := range map[string]string{
		"remember_recall":       "remember_recall_run7",
		"job change (v2)":       "job_change__v2__run7",
		"-lead":                 "_lead_run7",
		strings.Repeat("a", 80): strings.Repeat("a", 59) + "_run7",
	} {
		got := datasetName(Case{Name: name}, 7)
		if got != want || len(got) > 64 {
			t.Errorf("datasetName(%q) = %q, want %q", name, got, want)
		}
	}
}
