package agent_research_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
)

// fakeRemote is a Remote that plays a log of events: what a test pushes is what Follow delivers, in order, from the number asked.
type fakeRemote struct {
	mu        sync.Mutex
	cond      *sync.Cond
	events    []agent_research.RemoteEvent
	requests  []agent_research.RemoteRequest
	froms     []int // the `from` of every Follow
	cancels   int
	startErr  error
	cancelErr error
	// cutAfter, if > 0, makes the first Follow return nil (a stream that ended at its time limit) after that many events.
	cutAfter int
	// onCancel, if set, is called (without the lock) when the job is cancelled: it pushes the job's answer.
	onCancel func(f *fakeRemote)
}

func newFakeRemote() *fakeRemote {
	f := &fakeRemote{}
	f.cond = sync.NewCond(&f.mu)
	return f
}

func (f *fakeRemote) push(evs ...agent_research.RemoteEvent) {
	f.mu.Lock()
	for _, ev := range evs {
		ev.Seq = len(f.events)
		f.events = append(f.events, ev)
	}
	f.mu.Unlock()
	f.cond.Broadcast()
}

func (f *fakeRemote) Start(_ context.Context, req agent_research.RemoteRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.startErr != nil {
		return "", f.startErr
	}
	return "job1", nil
}

func (f *fakeRemote) Cancel(context.Context, string) error {
	f.mu.Lock()
	f.cancels++
	err, on := f.cancelErr, f.onCancel
	f.mu.Unlock()
	if on != nil && err == nil {
		on(f)
	}
	return err
}

func (f *fakeRemote) Follow(ctx context.Context, _ string, from int, fn func(agent_research.RemoteEvent) error) error {
	f.mu.Lock()
	f.froms = append(f.froms, from)
	first := len(f.froms) == 1
	f.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { f.mu.Lock(); f.mu.Unlock(); f.cond.Broadcast() })
	defer stop()
	for next, sent := from, 0; ; {
		f.mu.Lock()
		for next >= len(f.events) && ctx.Err() == nil {
			f.cond.Wait()
		}
		if ctx.Err() != nil {
			f.mu.Unlock()
			return ctx.Err()
		}
		ev := f.events[next]
		f.mu.Unlock()
		if err := fn(ev); err != nil {
			return err
		}
		next++
		if sent++; first && f.cutAfter > 0 && sent == f.cutAfter {
			return nil
		}
	}
}

func state(agent string, seq int, name, detail, errText string) agent_research.RemoteEvent {
	return agent_research.RemoteEvent{Type: agent_research.RemoteState, Agent: agent, StateSeq: seq, State: name, Step: 1, Detail: detail, Error: errText}
}

func status(s string) agent_research.RemoteEvent {
	return agent_research.RemoteEvent{Type: agent_research.RemoteStatus, Status: s}
}

func TestModalRunnerRecordsTheJobsEvents(t *testing.T) { runOnBackends(t, testModalRun) }

func testModalRun(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	remote := newFakeRemote()
	remote.cutAfter = 2 // the first stream ends early: the follower must resume with the next event
	m := agent_research.NewManager(ctx, d, ops, nil)
	m.UseRemote(remote)

	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "batteries", MaxSteps: 5, Model: "my-model", Dataset: "ds", Runner: "modal"})
	if err != nil {
		t.Fatal(err)
	}
	if started.Runner != core.RunnerModal || started.Status != core.StatusRunning || started.State != core.StateStarting || started.MaxSteps != 5 {
		t.Fatalf("started: %+v", started)
	}
	remote.push(
		status(agent_research.RemoteRunning),
		state("main", 1, core.StateStarting, "", ""), // the run has its own starting state
		state("main", 2, core.StateThinking, "step 1 of 5", ""),
		state("main", 3, core.StateSearching, "solid state", ""),
		agent_research.RemoteEvent{Type: agent_research.RemoteUsageEvent, Usage: &agent_research.RemoteUsage{Steps: 1, ToolCalls: 1, InputTokens: 10, OutputTokens: 5, CostUSD: 0.01}},
		state("main", 3, core.StateSearching, "solid state", "search failed"), // the state ended with an error, after it was reported
		state("kid", 2, core.StateReading, "https://example.com", ""),
		agent_research.RemoteEvent{Type: agent_research.RemoteLog, Message: "hello"},
		agent_research.RemoteEvent{Type: agent_research.RemoteStatus, Status: agent_research.RemoteCompleted, Report: "# Report",
			Usage: &agent_research.RemoteUsage{Steps: 4, ToolCalls: 3, InputTokens: 100, OutputTokens: 50, CostUSD: 0.25, Subagents: 1}},
	)
	m.Wait()

	run, err := m.Get(ctx, "u1", started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != core.StatusCompleted || run.Report != "# Report" || run.Runner != core.RunnerModal || run.Error != "" || run.FinishedAt == nil {
		t.Fatalf("run: %+v", run)
	}
	if run.Steps != 4 || run.ToolCalls != 3 || run.InputTokens != 100 || run.OutputTokens != 50 || run.CostUSD != 0.25 {
		t.Errorf("usage: %+v", run)
	}
	if got := stateNames(run.States); got != "starting,thinking,searching,reading,completed" {
		t.Fatalf("states: %s", got)
	}
	if s := run.States[2]; s.Error != "search failed" || s.Detail != "solid state" {
		t.Errorf("searching: %+v", s)
	}
	if s := run.States[3]; s.Detail != "[kid] https://example.com" {
		t.Errorf("a sub-agent's state is named in its detail: %+v", s)
	}
	if len(remote.requests) != 1 || remote.requests[0] != (agent_research.RemoteRequest{Topic: "batteries", MaxSteps: 5, Model: "my-model", Dataset: "ds"}) {
		t.Errorf("requests: %+v", remote.requests)
	}
	if len(remote.froms) < 2 || remote.froms[0] != 0 || remote.froms[1] != 2 {
		t.Errorf("the follower should resume where the stream ended: froms %v", remote.froms)
	}
	// The run does not count against the user's limit any more.
	if _, err := m.Start(ctx, "u1", agent_research.Options{Topic: "again", Runner: "modal"}); err != nil {
		t.Errorf("start again: %v", err)
	}
	m.Wait() // the fake plays its whole log to the second job too, which ends as the first did
}

func TestModalRunnerFailedJob(t *testing.T) { runOnBackends(t, testModalFailed) }

func testModalFailed(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	remote := newFakeRemote()
	m := agent_research.NewManager(ctx, d, ops, nil)
	m.UseRemote(remote)
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t", Runner: "modal"})
	if err != nil {
		t.Fatal(err)
	}
	remote.push(state("main", 2, core.StateThinking, "step 1 of 20", ""),
		agent_research.RemoteEvent{Type: agent_research.RemoteStatus, Status: agent_research.RemoteFailed, Error: "anthropic: 529 overloaded"})
	m.Wait()
	run, _ := m.Get(ctx, "u1", started.ID)
	if run.Status != core.StatusFailed || run.Error != "anthropic: 529 overloaded" || run.Report != "" || stateNames(run.States) != "starting,thinking,failed" {
		t.Fatalf("run: %+v states %s", run, stateNames(run.States))
	}
}

func TestModalRunnerStop(t *testing.T) { runOnBackends(t, testModalStop) }

func testModalStop(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	remote := newFakeRemote()
	remote.onCancel = func(f *fakeRemote) {
		f.push(agent_research.RemoteEvent{Type: agent_research.RemoteStatus, Status: agent_research.RemoteCancelled, Error: "stopped"})
	}
	m := agent_research.NewManager(ctx, d, ops, nil)
	m.UseRemote(remote)
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t", Runner: "modal"})
	if err != nil {
		t.Fatal(err)
	}
	remote.push(state("main", 2, core.StateThinking, "step 1 of 20", ""))
	if _, err := m.Stop(ctx, "u2", started.ID, "u2"); !isKind(err, agent_research.KindNotFound) {
		t.Fatalf("stop by another user: %v", err)
	}
	run, err := m.Stop(ctx, "u1", started.ID, "the test")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != core.StatusStopped || run.Error != "stopped by the test" || run.Report != "" || remote.cancels != 1 {
		t.Fatalf("stopped: %+v, cancels %d", run, remote.cancels)
	}
	if _, err := m.Stop(ctx, "u1", started.ID, "the test"); !isKind(err, agent_research.KindConflict) {
		t.Errorf("stopping a stopped run: %v", err)
	}
}

// A job that cannot be asked to stop does not keep the run: it ends as stopped, and says the job may go on.
func TestModalRunnerStopWhenTheJobCannotBeReached(t *testing.T) {
	runOnBackends(t, func(t *testing.T, d *sql.DB, ops agent_research.Ops) {
		ctx := context.Background()
		remote := newFakeRemote()
		remote.cancelErr = errors.New("sidecar down")
		m := agent_research.NewManager(ctx, d, ops, nil)
		m.UseRemote(remote)
		started, _ := m.Start(ctx, "u1", agent_research.Options{Topic: "t", Runner: "modal"})
		run, err := m.Stop(ctx, "", started.ID, "the test")
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != core.StatusStopped || run.Error == "" {
			t.Fatalf("run: %+v", run)
		}
	})
}

func TestModalRunnerStopOfTheServer(t *testing.T) {
	runOnBackends(t, func(t *testing.T, d *sql.DB, ops agent_research.Ops) {
		ctx, stop := context.WithCancel(context.Background())
		remote := newFakeRemote()
		m := agent_research.NewManager(ctx, d, ops, nil)
		m.UseRemote(remote)
		started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t", Runner: "modal"})
		if err != nil {
			t.Fatal(err)
		}
		stop()
		m.Wait()
		run, _ := m.Get(context.Background(), "u1", started.ID)
		if run.Status != core.StatusFailed || run.Error == "" || remote.cancels != 1 {
			t.Fatalf("run: %+v, cancels %d: the job must be cancelled so that it does not spend on", run, remote.cancels)
		}
	})
}

func TestModalRunnerStartErrors(t *testing.T) {
	runOnBackends(t, func(t *testing.T, d *sql.DB, ops agent_research.Ops) {
		ctx := context.Background()
		// No remote: unavailable (also with no LLM client, which the modal runner does not need), and a bad runner is invalid.
		m := agent_research.NewManager(ctx, d, ops, nil)
		if _, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t", Runner: "modal"}); !isKind(err, agent_research.KindUnavailable) {
			t.Fatalf("no remote: %v", err)
		}
		if _, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t", Runner: "lambda"}); !isKind(err, agent_research.KindInvalid) {
			t.Fatalf("bad runner: %v", err)
		}
		// A local run still needs the LLM.
		if _, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t"}); !isKind(err, agent_research.KindUnavailable) {
			t.Fatalf("local without llm: %v", err)
		}
		// A job that cannot be started leaves no run and no active slot behind.
		remote := newFakeRemote()
		remote.startErr = errors.New("the agent is not deployed")
		m.UseRemote(remote)
		for i := 0; i < agent_research.DefaultMaxActivePerUser+1; i++ {
			if _, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t", Runner: "modal"}); !isKind(err, agent_research.KindUnavailable) {
				t.Fatalf("start %d: %v", i, err)
			}
		}
		runs, err := m.List(ctx, "", agent_research.ListOptions{})
		if err != nil || len(runs) != 0 {
			t.Fatalf("runs: %+v, %v", runs, err)
		}
	})
}

// Without a runner a run is local, and says so.
func TestRunsAreLocalByDefault(t *testing.T) {
	runOnBackends(t, func(t *testing.T, d *sql.DB, ops agent_research.Ops) {
		ctx := context.Background()
		fake := &fakeAnthropic{main: []string{finalReport}}
		m := agent_research.NewManager(ctx, d, ops, fake.start(t))
		started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t"})
		if err != nil {
			t.Fatal(err)
		}
		m.Wait()
		run, _ := m.Get(ctx, "u1", started.ID)
		if started.Runner != core.RunnerLocal || run.Runner != core.RunnerLocal || run.Status != core.StatusCompleted {
			t.Fatalf("started %q, run %+v", started.Runner, run)
		}
	})
}
