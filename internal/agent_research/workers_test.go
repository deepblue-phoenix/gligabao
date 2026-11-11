package agent_research_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
)

const workersDef = `
name = "deep"
version = "1.0.0"
description = "Investigates with workers."
system_prompt = "DEEP PROMPT"
loop = "investigative"
[investigate]
max_iterations = 12
min_sources = 1
min_domains = 1
max_parallel = 2
`

// planTwo is the planning turn of the supervisor: two questions, both ready, so the loop hands them to workers.
func planTwo() string {
	return call(
		[2]string{"add_question", `{"parent_id":"q_001","question":"Why are batteries good?","priority":0.8,"validation_criteria":"a reason"}`},
		[2]string{"add_question", `{"parent_id":"q_001","question":"What do batteries cost?","priority":0.7,"validation_criteria":"a price"}`},
	)
}

// answered is the findings of a worker that answers its question with one claim.
func answered(url string) string {
	in := `{"status":"answered","answer":"It is so (c1).","claims":[{"key":"c1","text":"a claim from ` + url + `","sources":[{"url":"` + url + `","title":"T","independent":true,"quote":"q"}]}],"queries":[{"query":"query for ` + url + `"}]}`
	return call([2]string{"submit_findings", in})
}

// unverifiable is the findings of a worker that cannot do what it was asked (here: verify the claims of the other workers, the questions that the
// end of the exploration adds for the claims that are still UNVERIFIED).
const unverifiable = `{"status":"unresolvable","answer":"","notes":"not verifiable in the test"}`

func withVerifiers(scripts map[string][]string) map[string][]string {
	scripts["q_004"] = []string{call([2]string{"submit_findings", unverifiable})}
	scripts["q_005"] = []string{call([2]string{"submit_findings", unverifiable})}
	return scripts
}

// finishRun is the turn in which the supervisor finishes the questions the workers left in progress and ends exploration, then the report.
func finishRun() []string {
	return []string{
		call(
			[2]string{"resolve_question", `{"id":"q_002","answer":"they store energy","reason":"shallow"}`},
			[2]string{"resolve_question", `{"id":"q_003","answer":"they cost little","reason":"shallow"}`},
			[2]string{"resolve_question", `{"id":"q_001","answer":"batteries are good","reason":"shallow"}`},
			[2]string{"notes_scratch", `{"text":"Conclusion: batteries are good."}`},
			[2]string{"challenge", `{"target_type":"conclusion","negation":"batteries are not good","strategy":"negation","expected_if_false":"e","query":"batteries not good official"}`},
			[2]string{"web_search", `{"query":"batteries not good official"}`},
			[2]string{"record_challenge_result", `{"challenge_id":"ch_001","outcome":"survived"}`},
		),
		call([2]string{"finalize_report", `{"report":"# Report\nBatteries are good (s_001).\n\n## Gaps and assumptions\n- q_004 unresolvable: not verified\n- q_005 unresolvable: not verified"}`}),
	}
}

func startDeep(t *testing.T, d *sql.DB, ops agent_research.Ops, fake *teamFake) (*agent_research.Manager, core.Run) {
	t.Helper()
	ctx := context.Background()
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	m.NotesDir = filepath.Join(t.TempDir(), "notes")
	if err := m.UseAgentDefs(writeDefs(t, map[string]string{"deep.toml": workersDef})); err != nil {
		t.Fatal(err)
	}
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "batteries", Agent: "deep"})
	if err != nil {
		t.Fatal(err)
	}
	return m, *started
}

func TestInvestigativeLoopRunsIndependentQuestionsInWorkers(t *testing.T) {
	runOnBackends(t, testWorkers)
}

func testWorkers(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(withVerifiers(map[string][]string{
		"root":  append([]string{planTwo()}, finishRun()...),
		"q_002": {answered("https://a.example/x")},
		"q_003": {answered("https://b.example/y")},
	}))
	m, started := startDeep(t, d, ops, fake)
	m.Wait()

	root, err := m.Get(ctx, "u1", started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Status != core.StatusCompleted || root.Error != "" || root.Steps != 3 {
		t.Fatalf("root: %+v", root)
	}
	if !strings.Contains(root.Report, "(https://a.example/x)") {
		t.Errorf("the source of a worker's claim is in the report: %s", root.Report)
	}
	states := stateNames(root.States)
	if !strings.Contains(states, "delegating,waiting") {
		t.Errorf("states: %s", states)
	}
	// The supervisor made three model calls: planning, finishing the questions, the report. The questions were not worked on by it.
	if fake.calls["root"] != 3 {
		t.Errorf("supervisor calls: %d", fake.calls["root"])
	}
	// The workers are runs with a parent and the name of their question.
	subs, err := m.List(ctx, "u1", agent_research.ListOptions{ParentID: root.ID})
	if err != nil || len(subs) != 4 { // the two questions, and the two that verify their claims
		t.Fatalf("workers: %+v %v", subs, err)
	}
	for _, w := range subs {
		if w.ParentID == nil || *w.ParentID != root.ID || w.UserID != "u1" || w.Status != core.StatusCompleted || !strings.HasPrefix(w.Name, "q_00") ||
			!strings.HasPrefix(w.Reason, "question "+w.Name) || w.MaxSteps != 12 {
			t.Errorf("worker: %+v", w)
		}
		var in struct{ Status string }
		if err := json.Unmarshal([]byte(w.Report), &in); err != nil || in.Status == "" {
			t.Errorf("the report of a worker is its findings: %q", w.Report)
		}
		if got := w.Topic; !strings.Contains(got, "Your question ("+w.Name) || !strings.Contains(got, "Objective of the investigation: batteries") {
			t.Errorf("brief: %s", got)
		}
	}
	// A worker has the web tools and submit_findings, no loop tools, and the worker prompt.
	b := fake.body("q_002", 0)
	for _, want := range []string{`"name":"web_search"`, `"name":"fetch_url"`, `"name":"submit_findings"`, "You are a worker of an investigation"} {
		if !strings.Contains(b, want) {
			t.Errorf("worker request lacks %s: %s", want, b)
		}
	}
	if strings.Contains(b, `"name":"add_question"`) || strings.Contains(b, "spawn_subagent") {
		t.Errorf("a worker has no loop tools: %s", b)
	}
	// The claims of the workers are in the notes, UNVERIFIED, with their questions.
	runDocQueries(t, d, root.ID, strings.HasSuffix(t.Name(), "postgres"))
	if got := fake.body("root", 1); !strings.Contains(got, "a claim from https://a.example/x") || !strings.Contains(got, "UNVERIFIED") {
		t.Errorf("the notes the supervisor sees lack the claims of the workers")
	}
}

func TestStopOfAnAgentStopsItsWorkers(t *testing.T) { runOnBackends(t, testStopWorkers) }

func testStopWorkers(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(map[string][]string{"root": {planTwo()}})
	fake.hang["q_002"], fake.hang["q_003"] = true, true
	m, started := startDeep(t, d, ops, fake)
	waitFor(t, "the workers to work", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.calls["q_002"] == 1 && fake.calls["q_003"] == 1
	})

	if _, err := m.Stop(ctx, "", started.ID, "the test"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	root, _ := m.Get(ctx, "", started.ID)
	if root.Status != core.StatusStopped {
		t.Fatalf("root: %+v", root)
	}
	subs, _ := m.List(ctx, "", agent_research.ListOptions{ParentID: started.ID})
	if len(subs) != 2 {
		t.Fatalf("workers: %+v", subs)
	}
	for _, w := range subs {
		if w.Status != core.StatusStopped || !strings.Contains(w.Error, "spawned it") || w.FinishedAt == nil || w.FinishedAt.After(*root.FinishedAt) {
			t.Fatalf("worker: %+v", w)
		}
	}
	kids, err := m.ChildCounts(ctx, []int64{started.ID, subs[0].ID})
	if err != nil || kids[started.ID] != 2 || len(kids) != 1 {
		t.Fatalf("child counts: %v %v", kids, err)
	}
}

func TestStopOfAWorkerLeavesItsSupervisorWorking(t *testing.T) { runOnBackends(t, testStopWorker) }

func testStopWorker(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	// The supervisor has no script after the fan-out: what matters is that it gets there.
	fake := newTeamFake(map[string][]string{"root": {planTwo()}, "q_003": {answered("https://b.example/y")}})
	fake.hang["q_002"] = true
	m, started := startDeep(t, d, ops, fake)
	waitFor(t, "the worker to work", func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.calls["q_002"] == 1 })
	subs, _ := m.List(ctx, "", agent_research.ListOptions{ParentID: started.ID})
	var stopID int64
	for _, w := range subs {
		if w.Name == "q_002" {
			stopID = w.ID
		}
	}
	if stopID == 0 {
		t.Fatalf("workers: %+v", subs)
	}
	if _, err := m.Stop(ctx, "", stopID, "the test"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	w, _ := m.Get(ctx, "", stopID)
	if w.Status != core.StatusStopped || w.Error != "stopped by the test" {
		t.Fatalf("worker: %+v", w)
	}
	// A stopped worker does not end its supervisor: the loop went on to its next model call.
	if fake.calls["root"] < 2 {
		t.Errorf("the supervisor made %d calls: it stopped at the failed worker", fake.calls["root"])
	}
}

func TestWorkerTrajectoriesAreMergedIntoTheSupervisors(t *testing.T) {
	runOnBackends(t, testMergedTrajectories)
}

func testMergedTrajectories(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(withVerifiers(map[string][]string{
		"root":  append([]string{planTwo()}, finishRun()...),
		"q_002": {answered("https://a.example/x")},
		"q_003": {answered("https://b.example/y")},
	}))
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	m.NotesDir = filepath.Join(t.TempDir(), "notes")
	if err := m.UseAgentDefs(writeDefs(t, map[string]string{"deep.toml": workersDef})); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	got := map[int64]*atif.Trajectory{}
	m.UseTrajectories(func(id int64, tr *atif.Trajectory) {
		mu.Lock()
		got[id] = tr
		mu.Unlock()
	})
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "batteries", Agent: "deep"})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()

	if len(got) != 1 || got[started.ID] == nil {
		t.Fatalf("one trajectory, the root's, for the whole research: %v", got)
	}
	tr := got[started.ID]
	perAgent := map[string]int{}
	for i, s := range tr.Steps {
		if s.StepID != i+1 {
			t.Fatalf("step %d has id %d", i+1, s.StepID)
		}
		name, _ := s.Extra["agent"].(string)
		perAgent[name]++
		if name == "q_002" && (s.Extra["depth"] != 1 || s.Extra["parent_run_id"] != started.ID) {
			t.Fatalf("worker step: %+v", s.Extra)
		}
	}
	if perAgent["q_002"] != 3 || perAgent["q_003"] != 3 || perAgent["q_004"] != 3 || perAgent["main"] == 0 { // system, user, one model call each
		t.Fatalf("steps per agent: %v", perAgent)
	}
	data, err := atif.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Extra struct {
			Agents []struct {
				Agent string `json:"agent"`
				Depth int    `json:"depth"`
			} `json:"agents"`
		} `json:"extra"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if a := doc.Extra.Agents; len(a) != 5 || a[0].Agent != "main" || a[1].Depth != 1 {
		t.Fatalf("agents: %+v", doc.Extra.Agents)
	}
}
