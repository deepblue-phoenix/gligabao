package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/experiments"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// scriptedLLM answers by what the prompt asks for: the recall prompt gets an answer, anything else an extraction.
type scriptedLLM struct {
	answer string
	mu     sync.Mutex
	models []string // the Model of each request, in order
}

func (l *scriptedLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	l.mu.Lock()
	l.models = append(l.models, req.Model)
	l.mu.Unlock()
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
	memory_core.TrajectoryDir = atifDir
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
	if failed := runCases(cases, &scriptedLLM{answer: "Alice."}, nil, nil, false, nil, trackDB, tracker); failed != 1 {
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
	cases, err := loadCases(nil, "", "")
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
	memory_core.TrajectoryDir = atifDir
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
		if failed := runCases(cases, &scriptedLLM{answer: "Alice."}, nil, nil, false, nil, database, tr); failed != 0 {
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

// chunkingLLM is an offline stand-in for the LLM on a text of "<Name> lives in <City>" sentences: extraction lists
// those facts of the text it is given, a resolution call finds nothing, a recall answers with answer.
type chunkingLLM struct{ answer string }

var livesIn = regexp.MustCompile(`([A-Z][a-z]+ [A-Z][a-z]+) (?:lives in|resides in|makes a home in) ([A-Z][a-z]+)`)

func (l *chunkingLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	last := req.Messages[len(req.Messages)-1].Content
	var out string
	switch {
	case strings.Contains(last, "<graph>"):
		out = `{"answer":"` + l.answer + `","entities":[1],"edges":[]}`
	case strings.Contains(req.System, "You compare NEW facts"):
		out = `{"duplicates":[],"contradictions":[]}`
	default:
		var ents, edges []string
		for _, m := range livesIn.FindAllStringSubmatch(last, -1) {
			ents = append(ents, fmt.Sprintf(`{"name":%q,"type":"person"},{"name":%q,"type":"place"}`, m[1], m[2]))
			edges = append(edges, fmt.Sprintf(`{"source":%q,"target":%q,"relation":"lives_in","description":"%s lives in %s."}`, m[1], m[2], m[1], m[2]))
		}
		out = `{"entities":[` + strings.Join(ents, ",") + `],"edges":[` + strings.Join(edges, ",") + `]}`
	}
	return &ai.Response{Text: out, StopReason: "end_turn", Provider: ai.ProviderAnthropic, Model: "m"}, nil
}

// TestChunkingCase loads the chunking suite (whose text lives in a file) and runs its case offline: the corpus must
// need many chunks and the chunking checks must pass. Raising expect_min_chunks above what the corpus yields must fail.
func TestChunkingCase(t *testing.T) {
	dir := t.TempDir()
	atifDir = filepath.Join(dir, "atif")
	memory_core.TrajectoryDir = atifDir
	t.Cleanup(func() { atifDir = "" })

	cases, err := loadCases([]string{"chunking_register"}, "", "")
	if err != nil || len(cases) != 1 {
		t.Fatalf("%v, %v", cases, err)
	}
	c := cases[0]
	if len(c.Remember) != 1 || len(c.Remember[0].Text) < 20000 || c.ExpectMinChunks < 10 {
		t.Fatalf("case: %d texts, %d bytes, expect_min_chunks %d", len(c.Remember), len(c.Remember[0].Text), c.ExpectMinChunks)
	}
	c.ExpectEdges = nil // the offline extractor only knows the "lives in" facts

	database, err := db.Open(db.Config{SQLitePath: filepath.Join(dir, "eval.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	llm := &chunkingLLM{answer: "Silverlake Brewing Company, Brno, restoration binder"}
	tr, err := experiments.Start(ctx, database.Relational, experiments.Options{Name: "exp"})
	if err != nil {
		t.Fatal(err)
	}
	if failed := runCases([]Case{c}, llm, nil, nil, false, nil, database, tr); failed != 0 {
		t.Fatalf("failed = %d", failed)
	}
	if _, err := tr.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	full, err := experiments.GetRun(ctx, database.Relational, tr.Run.ID)
	if err != nil || len(full.Evals) != 1 || len(full.Evals[0].PassedChecks) < 7 {
		t.Fatalf("run: %+v, %v", full, err)
	}

	c.ExpectMinChunks = 1000
	tr, err = experiments.Start(ctx, database.Relational, experiments.Options{Name: "exp"})
	if err != nil {
		t.Fatal(err)
	}
	if failed := runCases([]Case{c}, llm, nil, nil, false, nil, database, tr); failed != 1 {
		t.Fatalf("with 1000 chunks expected: failed = %d, want 1", failed)
	}
}

func TestTextFileErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "s.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`[{"name":"a","remember":[{"text_file":"missing.txt"}]}]`)
	if _, err := loadCases(nil, dir, ""); err == nil {
		t.Fatal("missing text_file accepted")
	}
	write(`[{"name":"a","remember":[{"text":"x","text_file":"s.json"}]}]`)
	if _, err := loadCases(nil, dir, ""); err == nil {
		t.Fatal("text and text_file accepted")
	}
}

// TestCaseModels checks that the runner's -models are the default model of each remember stage, and that a case's own
// "models" win over them stage by stage; recall is not affected.
func TestCaseModels(t *testing.T) {
	dir := t.TempDir()
	atifDir = filepath.Join(dir, "atif")
	memory_core.TrajectoryDir = atifDir
	t.Cleanup(func() { atifDir = "" })

	database, err := db.Open(db.Config{SQLitePath: filepath.Join(dir, "eval.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	defaults, err := memory_core.ParseModels("extract_graph=model-default,resolve_edges=model-resolve")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory_core.ParseModels("bogus=x"); err == nil {
		t.Fatal("unknown stage accepted")
	}

	run1 := func(c Case) []string {
		llm := &scriptedLLM{answer: "Alice."}
		tr, err := experiments.Start(ctx, database.Relational, experiments.Options{Name: "exp"})
		if err != nil {
			t.Fatal(err)
		}
		if failed := runCases([]Case{c}, llm, nil, nil, false, defaults, database, tr); failed != 0 {
			t.Fatalf("failed = %d", failed)
		}
		if _, err := tr.Finish(ctx); err != nil {
			t.Fatal(err)
		}
		return llm.models
	}
	base := Case{Suite: "s", Name: "m1", Remember: []Artifact{{Text: "Alice is here."}}, Query: "Who?", ExpectContains: []string{"alice"}}

	// extract (remember) then answer (recall, no model).
	if got := fmt.Sprint(run1(base)); got != "[model-default ]" {
		t.Errorf("default models: request models = %s", got)
	}
	over := base
	over.Name, over.Models = "m2", map[string]string{"extract_graph": "model-case"}
	if got := fmt.Sprint(run1(over)); got != "[model-case ]" {
		t.Errorf("case models: request models = %s", got)
	}
	bad := base
	bad.Name, bad.Models = "m3", map[string]string{"nope": "x"}
	tr, err := experiments.Start(ctx, database.Relational, experiments.Options{Name: "exp"})
	if err != nil {
		t.Fatal(err)
	}
	if failed := runCases([]Case{bad}, &scriptedLLM{}, nil, nil, false, nil, database, tr); failed != 1 {
		t.Errorf("a case with an unknown stage: failed = %d, want 1", failed)
	}
}

// TestTimeseriesIngestCase runs the timeseries_ingest_aapl case with a scripted LLM: the checks are on what is
// stored, so no real model is needed, and the case has no query (no recall).
func TestTimeseriesIngestCase(t *testing.T) {
	dir := t.TempDir()
	atifDir = filepath.Join(dir, "atif")
	memory_core.TrajectoryDir = atifDir
	t.Cleanup(func() { atifDir = "" })

	cases, err := loadCases([]string{"timeseries_ingest_aapl"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	trackDB, err := db.Open(db.Config{SQLitePath: filepath.Join(dir, "eval.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer trackDB.Close()
	tracker, err := experiments.Start(context.Background(), trackDB.Relational, experiments.Options{Name: "ts"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(cases[0], tracker.Run.ID, trackDB, &scriptedLLM{}, nil, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Problems) > 0 || len(out.PassedChecks) < 8 || out.Answer != "" || len(out.Trajectories) != 1 {
		t.Fatalf("problems=%v passed=%v answer=%q trajectories=%d", out.Problems, out.PassedChecks, out.Answer, len(out.Trajectories))
	}
}
