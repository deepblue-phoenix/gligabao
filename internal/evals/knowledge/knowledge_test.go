package knowledge_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/evals"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/knowledge"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// testEvalsDir is the directory of the evals of the repository (relative to this package).
const testEvalsDir = "../../../build/evals_knowledge"

// scriptedLLM answers by what the prompt asks for: the query prompt gets an answer, anything else an extraction.
type scriptedLLM struct {
	answer string
	mu     sync.Mutex
	levels []string // the Reasoning of each request, in order
}

func (l *scriptedLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	l.mu.Lock()
	l.levels = append(l.levels, req.Reasoning)
	l.mu.Unlock()
	out := `{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`
	if len(req.Messages) > 0 && strings.Contains(req.Messages[len(req.Messages)-1].Content, "<graph>") {
		out = `{"answer":"` + l.answer + `","entities":[1],"edges":[]}`
	}
	return &ai.Response{Text: out, StopReason: "end_turn", Provider: ai.ProviderAnthropic, Model: "m", InputTokens: 10, OutputTokens: 5, CostUSD: 0.01}, nil
}

// env is an eval database with a runner on it, and the executor over it.
type env struct {
	t      *testing.T
	db     *db.DB
	r      *runner.Runner
	x      *knowledge.Executor
	atif   string
	llm    *scriptedLLM
	output strings.Builder
}

func newEnv(t *testing.T, answer string) *env {
	t.Helper()
	dir := t.TempDir()
	atif := filepath.Join(dir, "atif")
	memory_core.TrajectoryDir = atif
	database, err := db.Open(db.Config{SQLitePath: filepath.Join(dir, "eval.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	store, err := evals.StoreFor(database.Backend)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: database, r: runner.New(database.Relational, store), atif: atif, llm: &scriptedLLM{answer: answer}}
	e.x = &knowledge.Executor{DB: database, LLM: e.llm, TrajectoryDir: atif, Out: &e.output}
	return e
}

// run syncs the eval and runs it once on its own.
func (e *env) run(ev knowledge.Eval) (*core.EvalRun, *core.EvalDef) {
	e.t.Helper()
	ctx := context.Background()
	def, err := e.r.SyncEval(ctx, knowledge.Spec(ev))
	if err != nil {
		e.t.Fatal(err)
	}
	run, err := e.r.RunEval(ctx, def, e.x, runner.EvalOptions{Meta: &core.KnowledgeEvalRunMeta{}})
	if err != nil {
		e.t.Fatal(err)
	}
	return run, def
}

func kcase(name, text, query string, contains ...string) core.KnowledgeCase {
	c := core.KnowledgeCase{Name: name}
	c.Remember = []core.KnowledgeArtifact{{Text: text}}
	c.Query, c.ExpectContains = query, contains
	return c
}

func count(t *testing.T, d *sql.DB, query string, args ...any) (n int) {
	t.Helper()
	if err := d.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestExecutor runs two cases, one passing and one failing its expected answer, and checks what the eval run holds.
func TestExecutor(t *testing.T) {
	e := newEnv(t, "Alice.")
	bad := kcase("bad", "Alice is here.", "Who?", "Bob")
	bad.Remember = append(bad.Remember, core.KnowledgeArtifact{Text: "Alice again."})
	run, def := e.run(knowledge.Eval{Name: "s", Cases: []core.KnowledgeCase{kcase("good", "Alice is here.", "Who?", "alice"), bad}})

	if want := (core.Counts{Total: 2, Completed: 2, Passed: 1, Failed: 1}); run.Counts != want || run.Status != core.StatusCompleted {
		t.Fatalf("eval run: %+v", run)
	}
	if run.CostUSD <= 0 {
		t.Errorf("cost %v", run.CostUSD)
	}
	d := e.db.Relational
	var runID = run.ID
	if n := count(t, d, `SELECT COUNT(*) FROM gb_case_runs c JOIN gb_case_defs cd ON cd.id = c.def_id
		WHERE c.eval_run_id = ? AND ((cd.name = 'good' AND c.verdict = 'pass') OR (cd.name = 'bad' AND c.verdict = 'fail')) AND c.status = 'completed' AND c.finished_at IS NOT NULL`, runID); n != 2 {
		t.Errorf("case runs with their verdicts: %d", n)
	}
	// good: 2 trajectories (remember, query), 1 check passed; bad: 3 trajectories, 1 problem.
	for name, want := range map[string][3]int{"good": {2, 1, 0}, "bad": {3, 0, 1}} {
		var id int64
		if err := d.QueryRow(`SELECT c.id FROM gb_case_runs c JOIN gb_case_defs cd ON cd.id = c.def_id WHERE c.eval_run_id = ? AND cd.name = ?`, runID, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		var passed, problems, answer string
		var steps int
		if err := d.QueryRow(`SELECT passed_checks, problems, answer, steps FROM gb_case_runs_meta_knowledge WHERE case_run_id = ?`, id).Scan(&passed, &problems, &answer, &steps); err != nil {
			t.Fatal(err)
		}
		trajectories := count(t, d, `SELECT COUNT(*) FROM gb_case_runs_meta_knowledge_trajectories WHERE case_run_id = ?`, id)
		if trajectories != want[0] || strings.Count(passed, `","`)+btoi(passed != "[]") != want[1] || strings.Count(problems, `","`)+btoi(problems != "[]") != want[2] || answer != "Alice." || steps == 0 {
			t.Errorf("%s: trajectories %d, passed %s, problems %s, answer %q, steps %d", name, trajectories, passed, problems, answer, steps)
		}
	}
	// The trajectories are files, the first of a case is the remember and the last the query.
	rows, err := d.Query(`SELECT path, kind, seq FROM gb_case_runs_meta_knowledge_trajectories ORDER BY case_run_id, seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var path, kind string
		var seq int
		if err := rows.Scan(&path, &kind, &seq); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); err != nil || (kind != "remember" && kind != "query") {
			t.Errorf("trajectory %s %s %d: %v", path, kind, seq, err)
		}
	}
	// The eval run's metadata sums the cases.
	if n := count(t, d, `SELECT COUNT(*) FROM gb_eval_runs_meta_knowledge WHERE eval_run_id = ? AND remember_calls = 3 AND query_calls = 2 AND checks_passed = 1 AND checks_failed = 1 AND steps > 0 AND remember_cost_usd > 0 AND query_cost_usd > 0`, runID); n != 1 {
		t.Errorf("eval run meta: %d", n)
	}
	// The graph of a case is in its dataset.
	var datasetID int64
	if err := d.QueryRow(`SELECT m.dataset_id FROM gb_case_runs_meta_knowledge m JOIN gb_case_runs c ON c.id = m.case_run_id
		JOIN gb_case_defs cd ON cd.id = c.def_id WHERE cd.name = 'good' AND c.eval_run_id = ?`, runID).Scan(&datasetID); err != nil {
		t.Fatal(err)
	}
	all, err := relational.ListAllDatasets(context.Background(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	var ds *relational.Dataset
	for i := range all {
		if all[i].ID == datasetID {
			ds = &all[i]
		}
	}
	if ds == nil || ds.Name != knowledge.DatasetName("good", run.ID) {
		t.Fatalf("dataset %d: %+v", datasetID, ds)
	}
	if g, err := memory.ExportDatasetGraph(context.Background(), d, ds); err != nil || len(g.Entities) == 0 {
		t.Fatalf("graph: %+v %v", g, err)
	}
	if out := e.output.String(); !strings.Contains(out, "PASS good") || !strings.Contains(out, "FAIL bad") || !strings.Contains(out, `answer does not contain "Bob"`) {
		t.Errorf("output: %s", out)
	}
	_ = def
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestRunsAreIsolatedAndKept runs the same eval twice in the same eval database: the second run does not wipe the first one's
// datasets, and each run works in datasets of its own.
func TestRunsAreIsolatedAndKept(t *testing.T) {
	e := newEnv(t, "Alice.")
	ev := knowledge.Eval{Name: "s", Cases: []core.KnowledgeCase{kcase("c", "Alice is here.", "Who?", "alice")}}
	r1, _ := e.run(ev)
	r2, _ := e.run(ev)
	if r1.ID == r2.ID || r1.Counts.Passed != 1 || r2.Counts.Passed != 1 {
		t.Fatalf("runs: %+v %+v", r1, r2)
	}
	for _, id := range []core.EvalRunID{r1.ID, r2.ID} {
		name := knowledge.DatasetName("c", id)
		if _, err := relational.DatasetID(context.Background(), e.db.Relational, knowledge.EvalUser, name); err != nil {
			t.Fatalf("dataset %q of run %d: %v", name, id, err)
		}
	}
	// The def is the same one for both runs.
	if n := count(t, e.db.Relational, `SELECT COUNT(DISTINCT def_id) FROM gb_case_runs`); n != 1 {
		t.Errorf("case defs run: %d", n)
	}
}

func TestDatasetName(t *testing.T) {
	for name, want := range map[string]string{
		"remember_query":        "remember_query_run7",
		"job change (v2)":       "job_change__v2__run7",
		"-lead":                 "_lead_run7",
		strings.Repeat("a", 80): strings.Repeat("a", 59) + "_run7",
	} {
		got := knowledge.DatasetName(name, 7)
		if got != want || len(got) > 64 {
			t.Errorf("DatasetName(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestCaseReasoning checks that the runner's reasoning is the level of each stage, and that a case's own win, stage by stage.
func TestCaseReasoning(t *testing.T) {
	defaults, err := knowledge.ParseReasoning("extract_graph=low, query=High")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"bogus=low", "extract_graph=max", "extract_graph", "web_search=low"} {
		if _, err := knowledge.ParseReasoning(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if m, err := knowledge.ParseReasoning(""); m != nil || err != nil {
		t.Errorf("empty: %v, %v", m, err)
	}

	e := newEnv(t, "Alice.")
	levels := func(c core.KnowledgeCase, reasoning map[string]string) (got []string, run *core.EvalRun) {
		e.llm.levels, e.x.Reasoning = nil, reasoning
		run, _ = e.run(knowledge.Eval{Name: "s", Cases: []core.KnowledgeCase{c}})
		return e.llm.levels, run
	}
	base := kcase("r1", "Alice is here.", "Who?", "alice")
	if got, run := levels(base, defaults); run.Counts.Passed != 1 || fmt.Sprint(got) != "[low high]" { // extract, then answer
		t.Errorf("run levels: %v %+v", got, run.Counts)
	}
	over := kcase("r2", "Alice is here.", "Who?", "alice")
	over.Reasoning = map[string]string{"extract_graph": "none", "query": "medium"}
	if got, run := levels(over, defaults); run.Counts.Passed != 1 || fmt.Sprint(got) != "[none medium]" {
		t.Errorf("case levels: %v %+v", got, run.Counts)
	}
	part := kcase("r3", "Alice is here.", "Who?", "alice")
	part.Reasoning = map[string]string{"extract_graph": "medium"}
	if got, run := levels(part, defaults); run.Counts.Passed != 1 || fmt.Sprint(got) != "[medium high]" {
		t.Errorf("partial case levels: %v %+v", got, run.Counts)
	}
	if got, run := levels(kcase("r4", "Alice is here.", "Who?", "alice"), nil); run.Counts.Passed != 1 || fmt.Sprint(got) != "[none none]" {
		t.Errorf("default levels: %v %+v", got, run.Counts)
	}
}

// TestErroredCase checks that a case that cannot run is an errored case run with the reason, and the next case still runs.
func TestErroredCase(t *testing.T) {
	e := newEnv(t, "Alice.")
	bad := kcase("bad", "Alice is here.", "Who?", "alice")
	bad.Reasoning = map[string]string{"nope": "low"}
	missing := kcase("missing", "", "Who?", "alice")
	missing.Remember = []core.KnowledgeArtifact{{TextFile: "no-such-file.txt"}}
	run, _ := e.run(knowledge.Eval{Name: "s", Cases: []core.KnowledgeCase{bad, missing, kcase("ok", "Alice is here.", "Who?", "alice")}})
	if want := (core.Counts{Total: 3, Completed: 1, Errored: 2, Passed: 1}); run.Counts != want || run.Status != core.StatusCompleted {
		t.Fatalf("eval run: %+v", run)
	}
	if n := count(t, e.db.Relational, `SELECT COUNT(*) FROM gb_case_runs WHERE eval_run_id = ? AND status = 'errored' AND verdict = '' AND error <> ''`, run.ID); n != 2 {
		t.Errorf("errored case runs: %d", n)
	}
	if out := e.output.String(); !strings.Contains(out, "FAIL bad: reasoning") || !strings.Contains(out, "FAIL missing") {
		t.Errorf("output: %s", out)
	}
}

// cancelOnCall cancels a context when the LLM is first called.
type cancelOnCall struct {
	memory_core.LLM
	cancel context.CancelFunc
}

func (c cancelOnCall) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	c.cancel()
	return c.LLM.Complete(ctx, req)
}

// TestCancelled checks that a run whose context ends is cancelled: the case that was running is a cancelled case run, and the
// case that was not reached has none (it is pending).
func TestCancelled(t *testing.T) {
	e := newEnv(t, "Alice.")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.x.LLM = cancelOnCall{LLM: e.llm, cancel: cancel}
	ev := knowledge.Eval{Name: "s", Cases: []core.KnowledgeCase{kcase("a", "Alice is here.", "Who?", "alice"), kcase("b", "Alice is here.", "Who?", "alice")}}
	def, err := e.r.SyncEval(ctx, knowledge.Spec(ev))
	if err != nil {
		t.Fatal(err)
	}
	run, err := e.r.RunEval(ctx, def, e.x, runner.EvalOptions{Meta: &core.KnowledgeEvalRunMeta{}})
	if err == nil || run == nil || run.Status != core.StatusCancelled || run.Counts.Cancelled != 1 || run.Counts.Pending != 1 || run.FinishedAt == nil {
		t.Errorf("cancelled run: %+v %v", run, err)
	}
	if n := count(t, e.db.Relational, `SELECT COUNT(*) FROM gb_case_runs WHERE eval_run_id = ? AND status = 'cancelled'`, run.ID); n != 1 {
		t.Errorf("cancelled case runs: %d", n)
	}
}

func TestLoadEvals(t *testing.T) {
	evals, err := knowledge.LoadEvals(testEvalsDir)
	if err != nil || len(evals) == 0 {
		t.Fatalf("%v, %v", evals, err)
	}
	// Every case of the evals sets the reasoning of every stage, and of the query, explicitly to none, so a run does not depend
	// on the server's defaults and is the baseline for comparing levels.
	for _, ev := range evals {
		if ev.Name == "" || len(ev.Cases) == 0 {
			t.Errorf("eval %+v", ev)
		}
		for _, c := range ev.Cases {
			for _, stage := range knowledge.ReasoningStages() {
				if c.Reasoning[stage] != "none" {
					t.Errorf("%s/%s: reasoning[%s] = %q, want none", ev.Name, c.Name, stage, c.Reasoning[stage])
				}
			}
		}
		// The spec can be put in a database.
		if spec := knowledge.Spec(ev); len(spec.Cases) != len(ev.Cases) || spec.Type != core.TypeKnowledge {
			t.Errorf("spec of %s: %+v", ev.Name, spec)
		}
	}
	// A selection of one eval and one case.
	sel, err := knowledge.SelectEvals(evals, evals[0].Name, []string{evals[0].Cases[0].Name})
	if err != nil || len(sel) != 1 || len(sel[0].Cases) != 1 {
		t.Errorf("SelectEvals: %+v %v", sel, err)
	}
}

func TestTextFileErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "s.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"name":"s","cases":[{"name":"a","remember":[{"text_file":"missing.txt"}]}]}`)
	if _, err := knowledge.LoadEvals(dir); err == nil {
		t.Fatal("missing text_file accepted")
	}
	write(`{"name":"s","cases":[{"name":"a","remember":[{"text":"x","text_file":"s.json"}]}]}`)
	if _, err := knowledge.LoadEvals(dir); err == nil {
		t.Fatal("text and text_file accepted")
	}
	write(`{"name":"s","cases":[{"name":"a","remember":[{"text_file":"s.json"}]}]}`)
	if _, err := knowledge.LoadEvals(dir); err != nil {
		t.Fatalf("a text_file that exists: %v", err)
	}
}
