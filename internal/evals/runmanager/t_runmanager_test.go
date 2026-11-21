package runmanager_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runmanager"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
	"github.com/deepblue-phoenix/gligabao/internal/evals/usereval"
	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// fakeLLM answers by what the prompt asks for (the query prompt gets the answer, anything else an extraction). While gate is set, a
// call waits for it to be closed (or for its context to end).
type fakeLLM struct {
	mu    sync.Mutex
	gate  chan struct{}
	calls int
	cost  float64
}

func (l *fakeLLM) Complete(ctx context.Context,
	req llm.Request) (*llm.Response, error) {
	l.mu.Lock()
	l.calls++
	gate, cost := l.gate, l.cost
	l.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out := `{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`
	if len(req.Messages) > 0 && strings.Contains(req.Messages[len(req.Messages)-1].Content, "<graph>") {
		out = `{"answer":"Alice.","entities":[1],"edges":[]}`
	}
	return &llm.Response{
		Text:         out,
		StopReason:   "end_turn",
		Provider:     llm.ProviderAnthropic,
		Model:        "m",
		InputTokens:  10,
		OutputTokens: 5,
		CostUSD:      cost,
	}, nil
}

func (l *fakeLLM) setGate(g chan struct{}) {
	l.mu.Lock()
	l.gate = g
	l.mu.Unlock()
}

func (l *fakeLLM) called() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

type fakeEmbedder struct{ dims int }

func (f fakeEmbedder) EmbedBatchUsage(_ context.Context,
	texts []string) ([][]float32, *llm.EmbedUsage, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, f.dims)
		out[i][0] = 1
	}
	return out, &llm.EmbedUsage{
		Provider: llm.ProviderOpenAI,
		Model:    "test-embed",
	}, nil
}

// setup is a manager over a fresh database of the backend.
type setup struct {
	t      *testing.T
	d      *db.DB
	m      *runmanager.Manager
	lm     *fakeLLM
	stop   context.CancelFunc
	files  userfiles.FS
	traces string
}

func newSetup(t *testing.T,
	d *sql.DB,
	limits runmanager.Limits) *setup {
	t.Helper()
	backend := config.DBBackendSQLite
	if strings.Contains(t.Name(), "postgres") {
		backend = config.DBBackendPostgres
	}
	vs, err := vector.NewChromem(":memory:", 2)
	if err != nil {
		t.Fatal(err)
	}
	database := &db.DB{
		Relational: d,
		Graph:      d,
		Vector:     vs,
		Backend:    backend,
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &setup{
		t:      t,
		d:      database,
		lm:     &fakeLLM{cost: 0.01},
		stop:   stop,
		files:  userfiles.FS{Dir: t.TempDir()},
		traces: t.TempDir(),
	}
	m, err := runmanager.New(ctx,
		runmanager.Config{
			DB:    database,
			Files: s.files,
			LLM:   s.lm,
			Embedder: fakeEmbedder{
				dims: 2,
			},
			Prices:        llm.NewPriceTable(nil),
			TrajectoryDir: s.traces,
			Limits:        limits,
		})
	if err != nil {
		t.Fatal(err)
	}
	s.m = m
	t.Cleanup(func() {
		stop()
		m.Wait()
	})
	return s
}

// eval saves an eval of n cases c1..cn for the user.
func (s *setup) eval(user,
	name string,
	n int) {
	s.t.Helper()
	cases := make([]string, n)
	for i := range cases {
		cases[i] = `{"name":"c` + string(rune('1'+i)) + `","remember":[{"text":"Alice is here."}],"query":"Who?","expect_contains":["alice"]}`
	}
	data := `{"name":"` + name + `","cases":[` + strings.Join(cases, ",") + `]}`
	if _, err := usereval.Save(context.Background(),
		s.m.Env(),
		user,
		name,
		[]byte(data)); err != nil {
		s.t.Fatal(err)
	}
}

// wait polls the run of the user until it has the status (or fails after a few seconds).
func (s *setup) wait(user string,
	id core.EvalRunID,
	want ...core.Status) *core.EvalRun {
	s.t.Helper()
	var run *core.EvalRun
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		r, err := s.m.Store().GetEvalRun(context.Background(),
			s.d.Relational,
			user,
			id)
		if err != nil {
			s.t.Fatal(err)
		}
		run = r
		for _, w := range want {
			if r.Status == w {
				return r
			}
		}
	}
	s.t.Fatalf("run %d: status %s (%s), want one of %v",
		id,
		run.Status,
		run.Error,
		want)
	return nil
}

func TestRunCompletes(t *testing.T) { dbtest.Run(t, testRunCompletes) }

func testRunCompletes(t *testing.T,
	d *sql.DB) {
	s := newSetup(t, d, runmanager.Limits{})
	s.eval("alice", "e", 2)
	id, err := s.m.Start(context.Background(),
		"alice",
		"e",
		runmanager.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	run := s.wait("alice", id, core.StatusCompleted, core.StatusErrored)
	if run.Status != core.StatusCompleted || run.Counts.Passed != 2 || run.Counts.Total != 2 {
		t.Fatalf("run: %+v",
			run)
	}
	if run.RunnerID == "" || run.HeartbeatAt == nil || run.StartedAt == nil || run.FinishedAt == nil || run.CostUSD <= 0 {
		t.Errorf("bookkeeping: %+v",
			run)
	}
	if run.UserID != "alice" || run.ExperimentRunID != nil {
		t.Errorf("owner or experiment: %+v",
			run)
	}
	// Its datasets are eval datasets of the run, the trajectories are stored.
	ctx := context.Background()
	list, err := relational.ListEvalDatasets(ctx,
		d,
		"alice",
		int64(id))
	if err != nil || len(list) != 2 {
		t.Fatalf("eval datasets: %v %v",
			list,
			err)
	}
	if regular, _ := relational.ListDatasets(ctx, d, "alice", false); len(regular) != 0 {
		t.Errorf("regular datasets: %v",
			regular)
	}
	crs, err := s.m.Store().ListCaseRuns(ctx,
		d,
		"alice",
		id)
	if err != nil || len(crs) != 2 {
		t.Fatalf("case runs: %v %v",
			crs,
			err)
	}
	// Another user sees nothing of it.
	if _, err := s.m.Store().GetEvalRun(ctx, d, "bob", id); !errors.Is(err, evalsdb.ErrNotFound) {
		t.Errorf("run of another user: %v",
			err)
	}
	if err := s.m.Cancel(ctx, "bob", id); !errors.Is(err, evalsdb.ErrNotFound) {
		t.Errorf("cancel by another user: %v",
			err)
	}
	if err := s.m.Cancel(ctx, "alice", id); !errors.Is(err, evalsdb.ErrRunFinished) {
		t.Errorf("cancel of a finished run: %v",
			err)
	}
	// Selected cases only.
	id2, err := s.m.Start(ctx,
		"alice",
		"e",
		runmanager.RunOptions{
			Cases: []string{"c2"},
		})
	if err != nil {
		t.Fatal(err)
	}
	if r := s.wait("alice", id2, core.StatusCompleted, core.StatusErrored); r.Counts.Total != 1 {
		t.Errorf("selected run: %+v",
			r)
	}
}

func TestStartErrors(t *testing.T) { dbtest.Run(t, testStartErrors) }

func testStartErrors(t *testing.T,
	d *sql.DB) {
	s := newSetup(t, d, runmanager.Limits{})
	s.eval("alice", "e", 1)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		user, eval string
		opts       runmanager.RunOptions
		want       error
	}{
		"no such eval":     {"alice", "missing", runmanager.RunOptions{}, usereval.ErrNotFound},
		"empty user":       {"", "e", runmanager.RunOptions{}, usereval.ErrInvalidUser},
		"system user":      {core.SystemUser, "e", runmanager.RunOptions{}, usereval.ErrInvalidUser},
		"unsupported":      {"alice", "e", runmanager.RunOptions{Models: map[string]string{"query": "no-such-model"}}, runmanager.ErrBadOptions},
		"unknown stage":    {"alice", "e", runmanager.RunOptions{Reasoning: map[string]string{"nope": "low"}}, runmanager.ErrBadOptions},
		"unknown case":     {"alice", "e", runmanager.RunOptions{Cases: []string{"zzz"}}, runmanager.ErrBadOptions},
		"another user's":   {"bob", "e", runmanager.RunOptions{}, usereval.ErrNotFound},
		"bad reasoning":    {"alice", "e", runmanager.RunOptions{Reasoning: map[string]string{"query": "max"}}, runmanager.ErrBadOptions},
		"bad query models": {"alice", "e", runmanager.RunOptions{Models: map[string]string{"nope": "claude-haiku-4-5"}}, runmanager.ErrBadOptions},
	} {
		if _, err := s.m.Start(ctx, tc.user, tc.eval, tc.opts); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v",
				name,
				err,
				tc.want)
		}
	}
	if s.lm.called() != 0 {
		t.Errorf("the LLM was called by a start that failed: %d",
			s.lm.called())
	}
}

func TestCancel(t *testing.T) { dbtest.Run(t, testCancel) }

func testCancel(t *testing.T,
	d *sql.DB) {
	s := newSetup(t, d, runmanager.Limits{})
	s.lm.setGate(make(chan struct{})) // never opened: the run is stuck in its first LLM call
	s.eval("alice", "e", 3)
	ctx := context.Background()
	id, err := s.m.Start(ctx,
		"alice",
		"e",
		runmanager.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.wait("alice", id, core.StatusRunning)
	if err := s.m.Cancel(ctx, "alice", id); err != nil {
		t.Fatal(err)
	}
	run := s.wait("alice", id, core.StatusCancelled, core.StatusErrored, core.StatusCompleted)
	if run.Status != core.StatusCancelled || !run.CancelRequested {
		t.Fatalf("run: %+v",
			run)
	}
	for end := time.Now().Add(5 * time.Second); s.m.Running() != 0 && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
	}
	if s.m.Running() != 0 {
		t.Errorf("still running: %d",
			s.m.Running())
	}
}

// TestCancelSeenByHeartbeat: a cancel recorded by another server (only the flag is set) stops the run at its next beat.
func TestCancelSeenByHeartbeat(t *testing.T) { dbtest.Run(t, testCancelSeenByHeartbeat) }

func testCancelSeenByHeartbeat(t *testing.T,
	d *sql.DB) {
	s := newSetup(t, d, runmanager.Limits{
		HeartbeatInterval: 20 * time.Millisecond,
	})
	s.lm.setGate(make(chan struct{}))
	s.eval("alice", "e", 1)
	ctx := context.Background()
	id, err := s.m.Start(ctx,
		"alice",
		"e",
		runmanager.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.wait("alice", id, core.StatusRunning)
	if err := s.m.Store().RequestCancel(ctx, d, "alice", id); err != nil {
		t.Fatal(err)
	}
	if run := s.wait("alice", id, core.StatusCancelled); run.HeartbeatAt == nil {
		t.Errorf("no heartbeat: %+v",
			run)
	}
}

func TestShutdown(t *testing.T) { dbtest.Run(t, testShutdown) }

func testShutdown(t *testing.T,
	d *sql.DB) {
	s := newSetup(t, d, runmanager.Limits{})
	s.lm.setGate(make(chan struct{}))
	s.eval("alice", "e", 1)
	id, err := s.m.Start(context.Background(),
		"alice",
		"e",
		runmanager.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.wait("alice", id, core.StatusRunning)
	s.stop()
	s.m.Wait()
	run, err := s.m.Store().GetEvalRun(context.Background(),
		d,
		"alice",
		id)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != core.StatusCancelled || run.Error != runmanager.ReasonShutdown {
		t.Fatalf("run: %+v",
			run)
	}
}

func TestLimits(t *testing.T) { dbtest.Run(t, testLimits) }

func testLimits(t *testing.T,
	d *sql.DB) {
	s := newSetup(t, d, runmanager.Limits{
		MaxRunsPerUser: 1,
		MaxRuns:        2,
	})
	gate := make(chan struct{})
	s.lm.setGate(gate)
	for _, u := range []string{"alice", "bob", "carol"} {
		s.eval(u, "e", 1)
	}
	ctx := context.Background()
	idA, err := s.m.Start(ctx, "alice", "e", runmanager.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// One run per user.
	if _, err := s.m.Start(ctx, "alice", "e", runmanager.RunOptions{}); !errors.Is(err, runmanager.ErrTooManyRuns) {
		t.Errorf("second run of the user: %v",
			err)
	}
	// Another user is not held back by it, until the server is full.
	idB, err := s.m.Start(ctx, "bob", "e", runmanager.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.Start(ctx, "carol", "e", runmanager.RunOptions{}); !errors.Is(err, runmanager.ErrTooManyRuns) {
		t.Errorf("run over the limit of the server: %v",
			err)
	}
	close(gate)
	s.wait("alice", idA, core.StatusCompleted)
	s.wait("bob", idB, core.StatusCompleted)
	// A finished run frees the place.
	for end := time.Now().Add(5 * time.Second); s.m.Running() != 0 && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
	}
	id, err := s.m.Start(ctx, "carol", "e", runmanager.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.wait("carol", id, core.StatusCompleted)
}

func TestDailyCap(t *testing.T) { dbtest.Run(t, testDailyCap) }

func testDailyCap(t *testing.T,
	d *sql.DB) {
	s := newSetup(t, d, runmanager.Limits{
		MaxUserCostUSDPerDay: 0.05,
	})
	s.eval("alice", "e", 3) // 3 cases of 2 calls at $0.01: $0.06
	s.eval("bob", "e", 1)
	ctx := context.Background()
	id, err := s.m.Start(ctx, "alice", "e", runmanager.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run := s.wait("alice", id, core.StatusCompleted, core.StatusErrored); run.CostUSD < 0.05 {
		t.Fatalf("run: %+v",
			run)
	}
	if _, err := s.m.Start(ctx, "alice", "e", runmanager.RunOptions{}); !errors.Is(err, runmanager.ErrDailyCap) {
		t.Errorf("start over the daily cap: %v",
			err)
	}
	if id, err := s.m.Start(ctx, "bob", "e", runmanager.RunOptions{}); err != nil {
		t.Errorf("another user: %v",
			err)
	} else {
		s.wait("bob", id, core.StatusCompleted)
	}
}

func TestRunCostCap(t *testing.T) { dbtest.Run(t, testRunCostCap) }

func testRunCostCap(t *testing.T,
	d *sql.DB) {
	s := newSetup(t, d, runmanager.Limits{
		MaxRunCostUSD: 0.03, // a case costs $0.02 or more: the third case does not start
	})
	s.eval("alice", "e", 5)
	id, err := s.m.Start(context.Background(),
		"alice",
		"e",
		runmanager.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	run := s.wait("alice", id, core.StatusErrored, core.StatusCompleted)
	if run.Status != core.StatusErrored || run.Error != "cost cap reached" || run.Counts.Completed >= 5 {
		t.Fatalf("run: %+v",
			run)
	}
}

func TestRecover(t *testing.T) { dbtest.Run(t, testRecover) }

func testRecover(t *testing.T,
	d *sql.DB) {
	s := newSetup(t, d, runmanager.Limits{})
	s.eval("alice", "e", 2)
	ctx := context.Background()
	def, err := s.m.Store().GetEvalDefByName(ctx, d, "alice", "e")
	if err != nil {
		t.Fatal(err)
	}
	// A run of a server that is gone (its last beat is old), a run of a live server, and a run of the CLI (no runner).
	make := func(runnerID string,
		beat time.Duration) *core.EvalRun {
		run, err := s.m.Runner().CreateRun(ctx,
			def,
			runnerID,
			runner.EvalOptions{
				Meta: &core.KnowledgeEvalRunMeta{},
			})
		if err != nil {
			t.Fatal(err)
		}
		if runnerID != "" {
			if err := s.m.Store().ClaimRun(ctx, d, run.ID, runnerID, time.Now().Add(-beat)); err != nil {
				t.Fatal(err)
			}
		}
		return run
	}
	dead := make("gone:1:1", time.Hour)
	live := make("other:2:2", time.Second)
	cli := make("", 0)
	if err := s.m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	status := func(r *core.EvalRun) *core.EvalRun {
		got, err := s.m.Store().GetEvalRun(ctx, d, "alice", r.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := status(dead); got.Status != core.StatusErrored || got.Error != runmanager.ReasonStale {
		t.Errorf("stale run: %+v",
			got)
	}
	if got := status(live); got.Status != core.StatusRunning {
		t.Errorf("live run: %+v",
			got)
	}
	if got := status(cli); got.Status != core.StatusPending {
		t.Errorf("CLI run: %+v",
			got)
	}
}

func TestRetention(t *testing.T) { dbtest.Run(t, testRetention) }

func testRetention(t *testing.T,
	d *sql.DB) {
	s := newSetup(t, d, runmanager.Limits{
		DatasetRetention: time.Nanosecond,
	})
	s.eval("alice", "e", 2)
	ctx := context.Background()
	id, err := s.m.Start(ctx, "alice", "e", runmanager.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.wait("alice", id, core.StatusCompleted)
	time.Sleep(1100 * time.Millisecond) // the end of the run is recorded in whole seconds
	n, err := s.m.Retain(ctx)
	if err != nil || n != 1 {
		t.Fatalf("retain: %d %v",
			n,
			err)
	}
	if list, _ := relational.ListEvalDatasets(ctx, d, "alice", int64(id)); len(list) != 0 {
		t.Errorf("datasets after retention: %v",
			list)
	}
	// The run and its case runs stay.
	if crs, err := s.m.Store().ListCaseRuns(ctx, d, "alice", id); err != nil || len(crs) != 2 {
		t.Errorf("case runs: %v %v",
			crs,
			err)
	}
	if n, err := s.m.Retain(ctx); err != nil || n != 0 {
		t.Errorf("second retain: %d %v",
			n,
			err)
	}
}

// TestRerunPerUser: a user has one rerun at a time; another user's rerun is not held back by it.
func TestRerunPerUser(t *testing.T) { dbtest.Run(t, testRerunPerUser) }

func testRerunPerUser(t *testing.T,
	d *sql.DB) {
	s := newSetup(t, d, runmanager.Limits{
		MaxRunsPerUser: 5,
	})
	ctx := context.Background()
	src := map[string]core.CaseRunID{}
	for _, u := range []string{"alice", "bob"} {
		s.eval(u, "e", 1)
		id, err := s.m.Start(ctx, u, "e", runmanager.RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		s.wait(u, id, core.StatusCompleted)
		crs, err := s.m.Store().ListCaseRuns(ctx, d, u, id)
		if err != nil || len(crs) != 1 {
			t.Fatalf("case runs: %v %v",
				crs,
				err)
		}
		src[u] = crs[0].ID
	}
	gate := make(chan struct{})
	s.lm.setGate(gate)
	opts := core.RerunOptions{
		QueryModel: "claude-haiku-4-5",
	}
	if _, err := s.m.Rerun(ctx, "alice", src["alice"], opts); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.Rerun(ctx, "alice", src["alice"], opts); !errors.Is(err, core.ErrRerunBusy) {
		t.Errorf("second rerun of the user: %v",
			err)
	}
	if _, err := s.m.Rerun(ctx, "bob", src["bob"], opts); err != nil {
		t.Errorf("rerun of another user: %v",
			err)
	}
	// A user cannot rerun the case run of another.
	if _, err := s.m.Rerun(ctx, "bob", src["alice"], opts); !errors.Is(err, evalsdb.ErrNotFound) && !errors.Is(err, core.ErrRerunBusy) {
		t.Errorf("rerun of a case run of another user: %v",
			err)
	}
	close(gate)
	s.m.Rerunner().Wait()
}

var _ memory_core.LLM = (*fakeLLM)(nil)
