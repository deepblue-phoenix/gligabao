package agent_research_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// teamFake is a fake Messages API for agents that run at the same time: each agent has its own script of answers. The agent is
// told by its prompt (a sub-agent says its name; the rest is "root"), and each call gets the next answer of its script.
type teamFake struct {
	mu      sync.Mutex
	scripts map[string][]string
	calls   map[string]int
	bodies  map[string][]string
	// hang are the agents whose first call never answers, until their request is cancelled.
	hang map[string]bool
}

func (f *teamFake) start(t *testing.T) *ai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		key := "root"
		if i := strings.Index(body, `You are a sub-agent named \"`); i >= 0 {
			rest := body[i+len(`You are a sub-agent named \"`):]
			key = rest[:strings.Index(rest, `\"`)]
		}
		f.mu.Lock()
		f.calls[key]++
		n := f.calls[key]
		f.bodies[key] = append(f.bodies[key], body)
		var script []string
		script = f.scripts[key]
		hang := f.hang[key]
		f.mu.Unlock()
		if hang {
			<-r.Context().Done()
			return
		}
		if n > len(script) {
			http.Error(w, `{"type":"error","error":{"type":"api_error","message":"script ended"}}`, http.StatusInternalServerError)
			return
		}
		io.WriteString(w, script[n-1])
	}))
	t.Cleanup(srv.Close)
	c, err := ai.New("k", ai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newTeamFake(scripts map[string][]string) *teamFake {
	return &teamFake{scripts: scripts, calls: map[string]int{}, bodies: map[string][]string{}, hang: map[string]bool{}}
}

// call is an answer of the model that uses tools: pairs of name and input.
func call(tools ...[2]string) string {
	var blocks []string
	for i, t := range tools {
		blocks = append(blocks, `{"type":"tool_use","id":"t`+string(rune('a'+i))+`","name":"`+t[0]+`","input":`+t[1]+`}`)
	}
	return `{"model":"m","stop_reason":"tool_use","content":[` + strings.Join(blocks, ",") + `],"usage":{"input_tokens":1,"output_tokens":1}}`
}

func final(text string) string {
	return `{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"` + text + `"}],"usage":{"input_tokens":1,"output_tokens":1}}`
}

func spawn(name, task string) [2]string {
	return [2]string{"spawn_subagent", `{"name":"` + name + `","reason":"reason of ` + name + `","task":"` + task + `"}`}
}

func (f *teamFake) body(key string, i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.bodies[key]) {
		return ""
	}
	return f.bodies[key][i]
}

func TestSubagentsAreTrackedAndSiblingsExchangeMessages(t *testing.T) {
	runOnBackends(t, testSubagents)
}

func testSubagents(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(map[string][]string{
		"root": {
			call(spawn("beta", "find the market"), spawn("alpha", "find the history")), // beta first: it is a sibling when alpha starts
			call([2]string{"wait_subagents", `{}`}),
			final("combined report"),
		},
		"alpha": {call([2]string{"list_siblings", `{}`}), call([2]string{"send_message", `{"to":"beta","text":"market is huge"}`}), final("alpha report")},
		"beta":  {call([2]string{"read_messages", `{"wait_seconds":5}`}), final("beta report")},
	})
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))

	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "the topic", MaxSteps: 5, Model: "my-model"})
	if err != nil {
		t.Fatal(err)
	}
	if started.ParentID != nil || started.Name != "" {
		t.Fatalf("a research of a user has no parent: %+v", started)
	}
	m.Wait()

	root, err := m.Get(ctx, "u1", started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Status != core.StatusCompleted || root.Report != "combined report" {
		t.Fatalf("root: %+v", root)
	}
	if got := stateNames(root.States); got != "starting,thinking,spawning,spawning,thinking,waiting,thinking,completed" {
		t.Fatalf("root states: %s", got)
	}
	if s := root.States[2]; s.Detail != "beta: reason of beta" {
		t.Errorf("spawning: %+v", s)
	}

	// The sub-agents are runs like the others: the user's, with a parent and a name, listed and readable.
	subs, err := m.List(ctx, "u1", agent_research.ListOptions{ParentID: root.ID})
	if err != nil || len(subs) != 2 {
		t.Fatalf("sub-agents: %+v %v", subs, err)
	}
	byName := map[string]core.Run{}
	for _, s := range subs {
		byName[s.Name] = s
		if s.ParentID == nil || *s.ParentID != root.ID || s.UserID != "u1" || s.Status != core.StatusCompleted || s.Model != "my-model" || s.MaxSteps != agent_research.DefaultSubagentMaxSteps {
			t.Errorf("sub-agent: %+v", s)
		}
	}
	if byName["alpha"].Reason != "reason of alpha" || byName["beta"].Reason != "reason of beta" {
		t.Errorf("the reasons were not stored: %+v", byName)
	}
	if byName["alpha"].Topic != "find the history" || byName["alpha"].Report != "alpha report" || byName["beta"].Report != "beta report" {
		t.Errorf("sub-agents: %+v", byName)
	}
	all, _ := m.List(ctx, "u1", agent_research.ListOptions{})
	if len(all) != 3 {
		t.Errorf("all runs: %d", len(all))
	}
	beta, _ := m.Get(ctx, "u1", byName["beta"].ID)
	if got := stateNames(beta.States); got != "starting,thinking,waiting,thinking,completed" {
		t.Errorf("beta states: %s", got)
	}

	// A sub-agent learns who its siblings are and why they exist (and is not listed itself), and is told its own reason.
	if b := fake.body("alpha", 1); !strings.Contains(b, "beta [working]: reason of beta") || strings.Contains(b, "alpha [") {
		t.Errorf("alpha did not get its siblings: %s", b)
	}
	if b := fake.body("alpha", 0); !strings.Contains(b, `"name":"list_siblings"`) || !strings.Contains(b, "reason of alpha") {
		t.Errorf("alpha: tools or reason in the prompt: %s", b)
	}
	// The message of alpha reached beta; the parent got the reports of both.
	if b := fake.body("beta", 1); !strings.Contains(b, "From alpha") || !strings.Contains(b, "market is huge") {
		t.Errorf("beta did not get the message: %s", b)
	}
	if b := fake.body("root", 2); !strings.Contains(b, "alpha report") || !strings.Contains(b, "beta report") {
		t.Errorf("root did not get the reports: %s", b)
	}
	// Tools: the root can spawn but has no siblings; the sub-agents can do both. A sub-agent is told who it is.
	if b := fake.body("root", 0); !strings.Contains(b, `"name":"spawn_subagent"`) || !strings.Contains(b, `"name":"wait_subagents"`) || strings.Contains(b, `"name":"send_message"`) {
		t.Errorf("root tools: %s", b)
	}
	if b := fake.body("alpha", 0); !strings.Contains(b, `"name":"send_message"`) || !strings.Contains(b, `"name":"read_messages"`) || !strings.Contains(b, `"name":"spawn_subagent"`) {
		t.Errorf("sub-agent tools: %s", b)
	}
}

func TestOnlySiblingsCanBeMessaged(t *testing.T) { runOnBackends(t, testSiblingsOnly) }

func testSiblingsOnly(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(map[string][]string{
		"root": {call(spawn("a", "task a"), spawn("b", "task b")), call([2]string{"wait_subagents", `{}`}), final("done")},
		"a":    {call(spawn("a1", "task a1")), call([2]string{"wait_subagents", `{}`}), final("a done")},
		// a1 is a child of a and a cousin of b: it can reach neither its parent, nor b, nor itself.
		"a1": {call(
			[2]string{"send_message", `{"to":"b","text":"hi cousin"}`},
			[2]string{"send_message", `{"to":"a","text":"hi parent"}`},
			[2]string{"send_message", `{"to":"a1","text":"hi me"}`}), final("a1 done")},
		"b": {call([2]string{"send_message", `{"to":"a","text":"hi sibling"}`}), call([2]string{"send_message", `{"to":"root","text":"hi root"}`}), final("b done")},
	})
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	if _, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t", MaxSteps: 5}); err != nil {
		t.Fatal(err)
	}
	m.Wait()

	b := fake.body("a1", 1)
	if strings.Count(b, "you have no sibling called") != 2 || !strings.Contains(b, "you cannot message yourself") {
		t.Errorf("a1 reached someone it should not: %s", b)
	}
	// b to its sibling a: delivered, or a has ended already; never "no sibling".
	if b := fake.body("b", 1); strings.Contains(b, "no sibling") {
		t.Errorf("b to a: %s", b)
	}
	if b := fake.body("b", 2); !strings.Contains(b, "you have no sibling called") {
		t.Errorf("b reached the root: %s", b)
	}
	// The third generation: a1 has depth 2 and cannot spawn.
	if b := fake.body("a1", 0); strings.Contains(b, `"name":"spawn_subagent"`) || !strings.Contains(b, `"name":"send_message"`) {
		t.Errorf("a1 tools: %s", b)
	}
	runs, _ := m.List(ctx, "u1", agent_research.ListOptions{})
	if len(runs) != 4 {
		t.Fatalf("runs: %d", len(runs))
	}
	for _, r := range runs {
		if r.Status != core.StatusCompleted {
			t.Errorf("%s: %+v", r.Name, r)
		}
	}
}

func TestSpawnRejects(t *testing.T) { runOnBackends(t, testSpawnRejects) }

func testSpawnRejects(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	var calls [][2]string
	calls = append(calls, spawn("dup", "one"), spawn("dup", "two"), spawn("bad name!", "x"), spawn("empty", " "), [2]string{"spawn_subagent", `{"name":"noreason","task":"x"}`})
	for i := 0; i < agent_research.MaxSubagentsPerAgent; i++ { // dup took one place: the last of these is over the limit
		calls = append(calls, spawn("n"+string(rune('a'+i)), "task"))
	}
	fake := newTeamFake(map[string][]string{
		"root": {call(calls...), final("done")},
		"dup":  {final("r")}, "na": {final("r")}, "nb": {final("r")}, "nc": {final("r")}, "nd": {final("r")}, "ne": {final("r")},
	})
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	started, _ := m.Start(ctx, "u1", agent_research.Options{Topic: "t", MaxSteps: 3})
	m.Wait()

	b := fake.body("root", 1)
	for _, want := range []string{"called \\\"dup\\\" already", "name must be 1 to 40", "missing \\\"task\\\"", "missing \\\"reason\\\"", "the most one agent may have"} {
		if !strings.Contains(b, want) {
			t.Errorf("%q missing in %s", want, b)
		}
	}
	subs, _ := m.List(ctx, "u1", agent_research.ListOptions{ParentID: started.ID})
	if len(subs) != agent_research.MaxSubagentsPerAgent {
		t.Errorf("sub-agents: %d", len(subs))
	}
}

func TestSubagentsEndWithTheirParent(t *testing.T) { runOnBackends(t, testParentEnds) }

func testParentEnds(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(map[string][]string{
		"root": {call(spawn("slow", "never ends")), final("done without waiting")},
	})
	fake.hang["slow"] = true
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	started, _ := m.Start(ctx, "u1", agent_research.Options{Topic: "t", MaxSteps: 3})
	m.Wait()

	root, _ := m.Get(ctx, "u1", started.ID)
	if root.Status != core.StatusCompleted {
		t.Fatalf("root: %+v", root)
	}
	subs, _ := m.List(ctx, "u1", agent_research.ListOptions{ParentID: started.ID})
	if len(subs) != 1 || subs[0].Status != core.StatusFailed || !strings.Contains(subs[0].Error, "cancelled") || subs[0].FinishedAt == nil {
		t.Fatalf("sub-agent: %+v", subs)
	}
	if subs[0].FinishedAt.After(*root.FinishedAt) {
		t.Errorf("the parent finished before its sub-agent")
	}
}

func TestSubagentsDoNotCountForTheUserLimit(t *testing.T) { runOnBackends(t, testSubagentLimit) }

func testSubagentLimit(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(map[string][]string{
		"root": {call(spawn("a", "x"), spawn("b", "x"), spawn("c", "x"), spawn("d", "x")), call([2]string{"wait_subagents", `{"names":["a","zz"]}`}), call([2]string{"wait_subagents", `{"names":["a","d"]}`}), final("done")},
		"a":    {final("ra")}, "b": {final("rb")}, "c": {final("rc")}, "d": {final("rd")},
	})
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	m.MaxActivePerUser = 1
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t", MaxSteps: 5})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	subs, _ := m.List(ctx, "u1", agent_research.ListOptions{ParentID: started.ID})
	if len(subs) != 4 {
		t.Fatalf("sub-agents: %d", len(subs))
	}
	if b := fake.body("root", 2); !strings.Contains(b, `no sub-agent called \"zz\"`) {
		t.Errorf("unknown name: %s", b)
	}
	if b := fake.body("root", 3); !strings.Contains(b, "ra") || !strings.Contains(b, "rd") {
		t.Errorf("reports: %s", b)
	}
}

func TestChanMessenger(t *testing.T) {
	ctx := context.Background()
	mr := agent_research.NewChanMessenger()
	if err := mr.Open(1); err != nil {
		t.Fatal(err)
	}
	if err := mr.Open(1); err == nil {
		t.Error("opening twice should fail")
	}
	if err := agent_research.SendMessage(ctx, mr, agent_research.Message{From: 2, To: 3, Text: "x"}); !errors.Is(err, agent_research.ErrNoInbox) {
		t.Errorf("send to nobody: %v", err)
	}
	for _, s := range []string{"one", "two"} {
		if err := agent_research.SendMessage(ctx, mr, agent_research.Message{From: 2, FromName: "x", To: 1, Text: s}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := mr.Receive(ctx, 1, 0)
	if err != nil || len(got) != 2 || got[0].Text != "one" || got[1].Text != "two" || got[0].SentAt.IsZero() {
		t.Fatalf("receive: %+v %v", got, err)
	}
	if got, err := mr.Receive(ctx, 1, 0); err != nil || len(got) != 0 {
		t.Fatalf("empty: %+v %v", got, err)
	}
	// Waiting for a message that comes, and for one that does not.
	go func() {
		time.Sleep(20 * time.Millisecond)
		mr.Send(ctx, agent_research.Message{To: 1, Text: "late"})
	}()
	if got, err := mr.Receive(ctx, 1, 5*time.Second); err != nil || len(got) != 1 || got[0].Text != "late" {
		t.Fatalf("wait: %+v %v", got, err)
	}
	start := time.Now()
	if got, err := mr.Receive(ctx, 1, 30*time.Millisecond); err != nil || len(got) != 0 || time.Since(start) < 25*time.Millisecond {
		t.Fatalf("timeout: %+v %v", got, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := mr.Receive(cctx, 1, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	// A full inbox refuses, a closed one is gone.
	for i := 0; i < agent_research.InboxSize; i++ {
		if err := mr.Send(ctx, agent_research.Message{To: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := mr.Send(ctx, agent_research.Message{To: 1}); !errors.Is(err, agent_research.ErrInboxFull) {
		t.Errorf("full: %v", err)
	}
	mr.Close(1)
	if err := mr.Send(ctx, agent_research.Message{To: 1}); !errors.Is(err, agent_research.ErrNoInbox) {
		t.Errorf("closed: %v", err)
	}
	if _, err := mr.Receive(ctx, 1, 0); !errors.Is(err, agent_research.ErrNoInbox) {
		t.Errorf("receive closed: %v", err)
	}
}

// A database made before sub-agents gets the columns and the indexes when it is opened.
func TestOpenUpgradesAnOldSQLiteSchema(t *testing.T) {
	d, err := relational.Open(config.DBBackendSQLite, filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`CREATE TABLE gb_agent_research_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id TEXT NOT NULL, topic TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'running',
		state TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', max_steps INTEGER NOT NULL DEFAULT 0, steps INTEGER NOT NULL DEFAULT 0,
		tool_calls INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
		cost_usd REAL NOT NULL DEFAULT 0, report TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL, finished_at DATETIME)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO gb_agent_research_runs (user_id, topic, created_at) VALUES ('u1', 'old', '2026-11-01T10:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // twice: it is idempotent
		if err := agent_research.Open(config.DBBackendSQLite, d); err != nil {
			t.Fatal(err)
		}
	}
	run, err := agent_research.SQLiteOps.Store.GetRun(context.Background(), d, 1)
	if err != nil || run.Topic != "old" || run.ParentID != nil || run.Name != "" {
		t.Fatalf("old run: %+v %v", run, err)
	}
	child := &core.Run{UserID: "u1", Topic: "c", ParentID: &run.ID, Name: "x"}
	if err := agent_research.SQLiteOps.Store.CreateRun(context.Background(), d, child); err != nil {
		t.Fatal(err)
	}
	dup := &core.Run{UserID: "u1", Topic: "c", ParentID: &run.ID, Name: "x"}
	if err := agent_research.SQLiteOps.Store.CreateRun(context.Background(), d, dup); err == nil {
		t.Error("two sub-agents of a parent with one name should fail")
	}
}
