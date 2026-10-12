package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
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
	evalDBPath, atifDir = filepath.Join(dir, "eval.db"), filepath.Join(dir, "atif")
	memory.TrajectoryDir = atifDir
	t.Cleanup(func() { evalDBPath, atifDir = "", "" })

	cases := []Case{
		{Suite: "s1", Name: "good", Remember: []Artifact{{Text: "Alice is here."}}, Query: "Who?", ExpectContains: []string{"alice"}},
		{Suite: "s2", Name: "bad", Remember: []Artifact{{Text: "Alice is here."}, {Text: "Alice again."}}, Query: "Who?", ExpectContains: []string{"Bob"}},
	}

	trackDB, err := db.Open(db.Config{SQLitePath: evalDBPath})
	if err != nil {
		t.Fatal(err)
	}
	defer trackDB.Close()
	ctx := context.Background()
	tracker, err := experiments.Start(ctx, trackDB.Relational, experiments.Options{Name: "exp"})
	if err != nil {
		t.Fatal(err)
	}
	if failed := runCases(cases, &scriptedLLM{answer: "Alice."}, false, tracker); failed != 1 {
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
