package agent_research_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
)

// fakeAnthropic is a fake Messages API. The calls of the agent (no web search tool in the request) are answered with main, in order;
// the calls of its web_search tool (a request with the web search server tool) with a search answer that cost one search.
type fakeAnthropic struct {
	mu    sync.Mutex
	main  []string
	calls int
	// gate, if not nil, holds back the first call of the agent until it is closed.
	gate chan struct{}
	// status, if not 0, is the HTTP status of every call of the agent (with an error body).
	status int
}

const (
	toolUseSearch = `{"model":"m","stop_reason":"tool_use","content":[{"type":"text","text":"I will search"},{"type":"tool_use","id":"t1","name":"web_search","input":{"query":"solid state batteries"}}],"usage":{"input_tokens":10,"output_tokens":5}}`
	searchAnswer  = `{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"Found things."}],"usage":{"input_tokens":7,"output_tokens":3,"server_tool_use":{"web_search_requests":1}}}`
	finalReport   = `{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"# Report\nBatteries are good."}],"usage":{"input_tokens":20,"output_tokens":9}}`
)

func toolUseFetch(url string) string {
	return `{"model":"m","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t2","name":"fetch_url","input":{"url":"` + url + `"}}],"usage":{"input_tokens":12,"output_tokens":4}}`
}

func (f *fakeAnthropic) start(t *testing.T) *ai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "web_search_20250305") {
			w.Write([]byte(searchAnswer))
			return
		}
		f.mu.Lock()
		f.calls++
		n, gate, status := f.calls, f.gate, f.status
		f.mu.Unlock()
		if gate != nil && n == 1 {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		if status != 0 {
			w.WriteHeader(status)
			io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"overloaded"}}`)
			return
		}
		w.Write([]byte(f.main[n-1]))
	}))
	t.Cleanup(srv.Close)
	c, err := ai.New("k", ai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestResearchRecordsTheRunAndEveryState(t *testing.T) { runOnBackends(t, testResearch) }

func testResearch(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer page.Close()
	fake := &fakeAnthropic{main: []string{toolUseSearch, toolUseFetch(page.URL + "/gone"), finalReport}}
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))

	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "  solid state batteries  ", MaxSteps: 4, Model: "my-model"})
	if err != nil {
		t.Fatal(err)
	}
	if started.ID == 0 || started.Status != core.StatusRunning || started.State != core.StateStarting || started.Topic != "solid state batteries" ||
		started.MaxSteps != 4 || started.Model != "my-model" || started.UserID != "u1" || started.FinishedAt != nil {
		t.Fatalf("started: %+v", started)
	}
	m.Wait()

	run, err := m.Get(ctx, "u1", started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != core.StatusCompleted || run.State != core.StateCompleted || run.Report != "# Report\nBatteries are good." || run.Error != "" ||
		run.FinishedAt == nil || run.Steps != 3 || run.ToolCalls != 2 {
		t.Fatalf("finished: %+v", run)
	}
	// 10+12+20 tokens of the agent and 7 of the search; the search cost is the only cost (the model has no known price).
	if run.InputTokens != 49 || run.OutputTokens != 21 || run.CostUSD != ai.WebSearchRequestUSD {
		t.Fatalf("usage: in %d out %d cost %v", run.InputTokens, run.OutputTokens, run.CostUSD)
	}
	if got := stateNames(run.States); got != "starting,thinking,searching,thinking,reading,thinking,completed" {
		t.Fatalf("states: %s", got)
	}
	for i, s := range run.States {
		if s.Seq != i+1 || s.FinishedAt == nil {
			t.Errorf("state %d: %+v", i, s)
		}
	}
	if s := run.States[1]; s.Step != 1 || s.Detail != "step 1 of 4" {
		t.Errorf("thinking: %+v", s)
	}
	if s := run.States[2]; s.Detail != "solid state batteries" || s.Step != 1 || s.Error != "" {
		t.Errorf("searching: %+v", s)
	}
	// A page that cannot be read is a failed state, not a failed run.
	if s := run.States[4]; s.Detail != page.URL+"/gone" || s.Step != 2 || !strings.Contains(s.Error, "status 404") {
		t.Errorf("reading: %+v", s)
	}
	if s := run.States[5]; s.Step != 3 || s.Detail != "step 3 of 4" {
		t.Errorf("last thinking: %+v", s)
	}

	runs, err := m.List(ctx, "u1", agent_research.ListOptions{})
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID || runs[0].States != nil {
		t.Fatalf("list: %+v %v", runs, err)
	}
}

func TestLastStepWritesTheReport(t *testing.T) { runOnBackends(t, testLastStep) }

func testLastStep(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := &fakeAnthropic{main: []string{toolUseSearch, finalReport}}
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	run, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t", MaxSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	got, _ := m.Get(ctx, "u1", run.ID)
	if got.Status != core.StatusCompleted || stateNames(got.States) != "starting,thinking,searching,writing_report,completed" {
		t.Fatalf("states: %s (%s)", stateNames(got.States), got.Status)
	}
	if s := got.States[3]; s.Step != 2 || !strings.Contains(s.Detail, "the last") {
		t.Errorf("writing_report: %+v", s)
	}
}

func TestAFailedModelCallFailsTheRun(t *testing.T) { runOnBackends(t, testFailedRun) }

func testFailedRun(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := &fakeAnthropic{status: 500}
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	run, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	got, _ := m.Get(ctx, "u1", run.ID)
	if got.Status != core.StatusFailed || got.State != core.StateFailed || got.Error == "" || got.Report != "" || got.FinishedAt == nil || got.Steps != 1 {
		t.Fatalf("run: %+v", got)
	}
	if stateNames(got.States) != "starting,thinking,failed" || got.States[1].Error != got.Error || got.States[2].Error != got.Error {
		t.Fatalf("states: %+v", got.States)
	}
	if active, _ := m.List(ctx, "u1", agent_research.ListOptions{Active: true}); len(active) != 0 {
		t.Fatalf("active: %+v", active)
	}
}

func TestListsTheActiveAgentsAndLimitsThePerUser(t *testing.T) { runOnBackends(t, testActive) }

func testActive(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	gate := make(chan struct{})
	fake := &fakeAnthropic{main: []string{finalReport, finalReport, finalReport}, gate: gate}
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	m.MaxActivePerUser = 1

	first, err := m.Start(ctx, "u1", agent_research.Options{Topic: "one"})
	if err != nil {
		t.Fatal(err)
	}
	// One agent of u1 is working: a second one of u1 is refused, one of u2 is not.
	_, err = m.Start(ctx, "u1", agent_research.Options{Topic: "two"})
	var e *agent_research.Error
	if !errors.As(err, &e) || e.Kind != agent_research.KindConflict {
		t.Fatalf("second start: %v", err)
	}
	// u2's agent is the second call of the fake: the first (held) one is u1's.
	if _, err := m.Start(ctx, "u2", agent_research.Options{Topic: "three"}); err != nil {
		t.Fatal(err)
	}

	active, err := m.List(ctx, "u1", agent_research.ListOptions{Active: true})
	if err != nil || len(active) != 1 || active[0].ID != first.ID || active[0].Status != core.StatusRunning {
		t.Fatalf("active of u1: %+v %v", active, err)
	}
	if all, _ := m.List(ctx, "", agent_research.ListOptions{Active: true}); len(all) != 2 {
		t.Fatalf("active of all: %+v", all)
	}
	// Whatever u1 asks, it does not see u2's.
	if mine, _ := m.List(ctx, "u1", agent_research.ListOptions{}); len(mine) != 1 {
		t.Fatalf("runs of u1: %+v", mine)
	}
	if _, err := m.Get(ctx, "u2", first.ID); !isKind(err, agent_research.KindNotFound) {
		t.Fatalf("get of another user's run: %v", err)
	}

	close(gate)
	m.Wait()
	if active, _ := m.List(ctx, "", agent_research.ListOptions{Active: true}); len(active) != 0 {
		t.Fatalf("active after: %+v", active)
	}
	done, _ := m.List(ctx, "u1", agent_research.ListOptions{Status: "completed"})
	if len(done) != 1 || done[0].Status != core.StatusCompleted {
		t.Fatalf("completed: %+v", done)
	}
	// The slot is free again.
	fake.mu.Lock()
	fake.main = append(fake.main, finalReport)
	fake.mu.Unlock()
	if _, err := m.Start(ctx, "u1", agent_research.Options{Topic: "four"}); err != nil {
		t.Fatalf("start after the end: %v", err)
	}
	m.Wait()
}

func isKind(err error, k agent_research.Kind) bool {
	var e *agent_research.Error
	return errors.As(err, &e) && e.Kind == k
}

func TestStopOfTheServerFailsTheRunningAgents(t *testing.T) { runOnBackends(t, testStop) }

func testStop(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx, stop := context.WithCancel(context.Background())
	fake := &fakeAnthropic{main: []string{finalReport}, gate: make(chan struct{})} // never opened
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	run, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	m.Wait()
	got, _ := m.Get(context.Background(), "u1", run.ID)
	if got.Status != core.StatusFailed || !strings.Contains(got.Error, "interrupted") || stateNames(got.States) != "starting,thinking,failed" {
		t.Fatalf("run: %+v", got)
	}
	if _, err := m.Start(context.Background(), "u1", agent_research.Options{Topic: "t"}); !isKind(err, agent_research.KindUnavailable) {
		t.Fatalf("start after stop: %v", err)
	}
}

func TestRunsLeftWorkingByADeadProcessAreFailedOnStart(t *testing.T) { runOnBackends(t, testRestart) }

func testRestart(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	run := &core.Run{UserID: "u1", Topic: "t", State: core.StateStarting}
	if err := ops.Store.CreateRun(ctx, d, run); err != nil {
		t.Fatal(err)
	}
	ops.Store.EnterState(ctx, d, run.ID, core.StateReading, 1, "http://x")
	m := agent_research.NewManager(ctx, d, ops, nil)
	got, err := m.Get(ctx, "u1", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != core.StatusFailed || !strings.Contains(got.Error, "server stopped") || stateNames(got.States) != "reading,failed" {
		t.Fatalf("run: %+v", got)
	}
}

func TestStartRejects(t *testing.T) { runOnBackends(t, testStartRejects) }

func testStartRejects(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	m := agent_research.NewManager(ctx, d, ops, (&fakeAnthropic{}).start(t))
	for name, opts := range map[string]agent_research.Options{
		"no topic":         {Topic: "   "},
		"long topic":       {Topic: strings.Repeat("é", agent_research.MaxTopicLen+1)},
		"negative steps":   {Topic: "t", MaxSteps: -1},
		"too many steps":   {Topic: "t", MaxSteps: agent_research.MaxMaxSteps + 1},
		"model with space": {Topic: "t", Model: "a b"},
	} {
		if _, err := m.Start(ctx, "u1", opts); !isKind(err, agent_research.KindInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if runs, _ := m.List(ctx, "", agent_research.ListOptions{}); len(runs) != 0 {
		t.Errorf("rejected requests were recorded: %+v", runs)
	}
	// Without an LLM there is no agent.
	if _, err := agent_research.NewManager(ctx, d, ops, nil).Start(ctx, "u1", agent_research.Options{Topic: "t"}); !isKind(err, agent_research.KindUnavailable) {
		t.Errorf("no client: %v", err)
	}
	for _, o := range []agent_research.ListOptions{{Active: true, Status: "failed"}, {Status: "bogus"}} {
		if _, err := m.List(ctx, "u1", o); !isKind(err, agent_research.KindInvalid) {
			t.Errorf("list %+v: %v", o, err)
		}
	}
	if _, err := m.Get(ctx, "u1", 12345); !isKind(err, agent_research.KindNotFound) {
		t.Errorf("get unknown: %v", err)
	}
}

func TestRunJSON(t *testing.T) {
	b, err := json.Marshal(core.Run{ID: 1, Status: core.StatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"status":"running"`, `"state":""`, `"report":""`, `"finished_at":null`, `"cost_usd":0`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("%s missing in %s", key, b)
		}
	}
	if strings.Contains(string(b), `"states"`) {
		t.Errorf("a run without states has no states key: %s", b)
	}
}
