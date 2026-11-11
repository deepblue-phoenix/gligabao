package agent_research_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

// waitFor polls cond until it holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStopEndsAnActiveAgent(t *testing.T) { runOnBackends(t, testStopAgent) }

func testStopAgent(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := &fakeAnthropic{main: []string{finalReport}, gate: make(chan struct{})} // never opened: the first call hangs
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the agent to call the model", func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.calls == 1 })

	if _, err := m.Stop(ctx, "u2", started.ID, "u2"); !isKind(err, agent_research.KindNotFound) {
		t.Fatalf("stop by another user: %v", err)
	}
	if _, err := m.Stop(ctx, "", 12345, "test"); !isKind(err, agent_research.KindNotFound) {
		t.Fatalf("stop unknown: %v", err)
	}
	run, err := m.Stop(ctx, "u1", started.ID, "the test")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != core.StatusStopped || run.Error != "stopped by the test" || run.FinishedAt == nil || run.Report != "" {
		t.Fatalf("stopped: %+v", run)
	}
	got, _ := m.Get(ctx, "u1", started.ID)
	if got.State != core.StateStopped || stateNames(got.States) != "starting,thinking,stopped" {
		t.Fatalf("states: %+v", got)
	}
	if last := got.States[len(got.States)-1]; last.Error != "stopped by the test" {
		t.Errorf("terminal state: %+v", last)
	}
	// It has ended: it cannot be stopped again, and its slot is free.
	if _, err := m.Stop(ctx, "", started.ID, "the test"); !isKind(err, agent_research.KindConflict) {
		t.Fatalf("stop again: %v", err)
	}
	m.Wait()
	if n, err := m.Counts(ctx, ""); err != nil || n[core.StatusStopped] != 1 || n[core.StatusRunning] != 0 {
		t.Fatalf("counts: %v %v", n, err)
	}
	if stopped, _ := m.List(ctx, "", agent_research.ListOptions{Status: "stopped"}); len(stopped) != 1 {
		t.Fatalf("stopped list: %+v", stopped)
	}
	if fin, _ := m.List(ctx, "", agent_research.ListOptions{Finished: true}); len(fin) != 1 {
		t.Fatalf("finished list: %+v", fin)
	}
}

func TestCountRunsAndChildCountsOfTheStore(t *testing.T) { runOnBackends(t, testCounts) }

func testCounts(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	st := ops.Store
	root := &core.Run{UserID: "u1", Topic: "r", State: core.StateStarting}
	other := &core.Run{UserID: "u2", Topic: "o", State: core.StateStarting}
	for _, r := range []*core.Run{root, other} {
		if err := st.CreateRun(ctx, d, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"a", "b"} {
		if err := st.CreateRun(ctx, d, &core.Run{UserID: "u1", Topic: n, ParentID: &root.ID, Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := st.FinishRun(ctx, d, other.ID, core.StatusStopped, "", "stopped by me"); err != nil || !ok {
		t.Fatalf("finish stopped: %v %v", ok, err)
	}
	count := func(f core.RunFilter) int {
		n, err := st.CountRuns(ctx, d, f)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(core.RunFilter{}); n != 4 {
		t.Errorf("all: %d", n)
	}
	if n := count(core.RunFilter{UserID: "u1"}); n != 3 {
		t.Errorf("u1: %d", n)
	}
	if n := count(core.RunFilter{RootsOnly: true}); n != 2 {
		t.Errorf("roots: %d", n)
	}
	if n := count(core.RunFilter{Statuses: []core.Status{core.StatusStopped}}); n != 1 {
		t.Errorf("stopped: %d", n)
	}
	if n := count(core.RunFilter{ParentID: &root.ID, Statuses: []core.Status{core.StatusRunning}}); n != 2 {
		t.Errorf("children: %d", n)
	}
	kids, err := st.ChildCounts(ctx, d, []int64{root.ID, other.ID})
	if err != nil || len(kids) != 1 || kids[root.ID] != 2 {
		t.Errorf("child counts: %v %v", kids, err)
	}
	if kids, err := st.ChildCounts(ctx, d, nil); err != nil || len(kids) != 0 {
		t.Errorf("no ids: %v %v", kids, err)
	}
}

// A database made before the status 'stopped' has a CHECK that forbids it: opening rebuilds the table, and keeps its runs, its states
// (which reference it), its indexes and the numbering of the IDs.
func TestOpenRebuildsAnOldSQLiteStatusCheck(t *testing.T) {
	ctx := context.Background()
	d, err := relational.Open(config.DBBackendSQLite, filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, stmt := range []string{
		`CREATE TABLE gb_agent_research_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT, user_id TEXT NOT NULL,
			parent_id INTEGER REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE, name TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '',
			topic TEXT NOT NULL CHECK (topic <> ''), status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed')),
			state TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', max_steps INTEGER NOT NULL DEFAULT 0, steps INTEGER NOT NULL DEFAULT 0,
			tool_calls INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd REAL NOT NULL DEFAULT 0, report TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL, finished_at DATETIME)`,
		`CREATE TABLE gb_agent_research_states (
			id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
			seq INTEGER NOT NULL CHECK (seq >= 1), state TEXT NOT NULL CHECK (state <> ''), step INTEGER NOT NULL DEFAULT 0 CHECK (step >= 0),
			detail TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', started_at DATETIME NOT NULL, finished_at DATETIME, UNIQUE (run_id, seq))`,
		`INSERT INTO gb_agent_research_runs (user_id, topic, status, created_at) VALUES ('u1', 'parent', 'completed', '2026-11-01T10:00:00.000Z')`,
		`INSERT INTO gb_agent_research_runs (user_id, parent_id, name, topic, created_at) VALUES ('u1', 1, 'kid', 'child', '2026-11-01T10:00:01.000Z')`,
		`INSERT INTO gb_agent_research_states (run_id, seq, state, started_at) VALUES (1, 1, 'starting', '2026-11-01T10:00:00.000Z'), (2, 1, 'starting', '2026-11-01T10:00:01.000Z')`,
	} {
		if _, err := d.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ { // twice: the second finds nothing to rebuild
		if err := agent_research.Open(config.DBBackendSQLite, d); err != nil {
			t.Fatal(err)
		}
	}
	st := agent_research.SQLiteOps.Store
	if run, err := st.GetRun(ctx, d, 2); err != nil || run.Topic != "child" || run.ParentID == nil || *run.ParentID != 1 || run.Name != "kid" {
		t.Fatalf("run 2: %+v %v", run, err)
	}
	if states, err := st.ListStates(ctx, d, 1); err != nil || len(states) != 1 {
		t.Fatalf("states of run 1 were lost: %v %v", states, err)
	}
	if ok, err := st.FinishRun(ctx, d, 2, core.StatusStopped, "", "stopped by me"); err != nil || !ok {
		t.Fatalf("stopped is still refused: %v %v", ok, err)
	}
	next := &core.Run{UserID: "u1", Topic: "next"}
	if err := st.CreateRun(ctx, d, next); err != nil || next.ID != 3 {
		t.Fatalf("next run: %+v %v", next, err)
	}
	var idx int
	if err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'gb_agent_research_runs' AND name LIKE 'idx_gb_agent_research_runs_%'`).Scan(&idx); err != nil || idx != 4 {
		t.Errorf("indexes: %d %v", idx, err)
	}
	var fk int
	if err := d.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign keys left off: %d %v", fk, err)
	}
}
