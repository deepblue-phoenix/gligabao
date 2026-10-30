package agent_research_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

// runOnBackends runs fn on a fresh database of every backend, with the Ops of the backend.
func runOnBackends(t *testing.T, fn func(t *testing.T, d *sql.DB, ops agent_research.Ops)) {
	t.Helper()
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		ops := agent_research.SQLiteOps
		if strings.HasSuffix(t.Name(), "postgres") {
			ops = agent_research.PostgresOps
		}
		fn(t, d, ops)
	})
}

func stateNames(states []core.State) string {
	var names []string
	for _, s := range states {
		names = append(names, s.State)
	}
	return strings.Join(names, ",")
}

func TestStore(t *testing.T) { runOnBackends(t, testStore) }

func testStore(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	st := ops.Store

	run := &core.Run{UserID: "u1", Topic: "batteries", Model: "m1", MaxSteps: 5, State: core.StateStarting}
	if err := st.CreateRun(ctx, d, run); err != nil {
		t.Fatal(err)
	}
	if run.ID == 0 || run.Status != core.StatusRunning || run.CreatedAt.IsZero() {
		t.Fatalf("created: %+v", run)
	}

	// Entering a state ends the one before it and sets the state of the run.
	s1, err := st.EnterState(ctx, d, run.ID, core.StateStarting, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := st.EnterState(ctx, d, run.ID, core.StateSearching, 1, "solid state")
	if err != nil {
		t.Fatal(err)
	}
	if s1.Seq != 1 || s2.Seq != 2 || s2.ID == s1.ID {
		t.Fatalf("seq: %+v %+v", s1, s2)
	}
	if err := st.CloseState(ctx, d, s2.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseState(ctx, d, s2.ID, "again"); err != nil { // already closed: left as it is
		t.Fatal(err)
	}
	if err := st.CloseState(ctx, d, 999999, ""); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("close unknown: %v", err)
	}
	if _, err := st.EnterState(ctx, d, run.ID, core.StateThinking, 2, "step 2"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateUsage(ctx, d, run.ID, db.Usage{Steps: 2, ToolCalls: 1, InputTokens: 30, OutputTokens: 7, CostUSD: 0.25}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateUsage(ctx, d, 999999, db.Usage{}); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("usage of unknown: %v", err)
	}

	got, err := st.GetRun(ctx, d, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != core.StateThinking || got.Status != core.StatusRunning || got.Steps != 2 || got.ToolCalls != 1 || got.InputTokens != 30 ||
		got.OutputTokens != 7 || got.CostUSD != 0.25 || got.Model != "m1" || got.MaxSteps != 5 || got.FinishedAt != nil ||
		!got.CreatedAt.Equal(run.CreatedAt) {
		t.Fatalf("run: %+v", got)
	}
	states, err := st.ListStates(ctx, d, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stateNames(states) != "starting,searching,thinking" || states[0].FinishedAt == nil || states[1].FinishedAt == nil || states[1].Error != "boom" ||
		states[1].Detail != "solid state" || states[1].Step != 1 || states[2].FinishedAt != nil || states[0].FinishedAt.Before(states[0].StartedAt) {
		t.Fatalf("states: %+v", states)
	}

	// Finishing ends the state the run is in, records the terminal state, and happens once.
	ok, err := st.FinishRun(ctx, d, run.ID, core.StatusCompleted, "the report", "")
	if err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	if ok, err := st.FinishRun(ctx, d, run.ID, core.StatusFailed, "", "late"); err != nil || ok {
		t.Fatalf("second finish: %v %v", ok, err)
	}
	if _, err := st.FinishRun(ctx, d, 999999, core.StatusFailed, "", "x"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("finish unknown: %v", err)
	}
	if _, err := st.FinishRun(ctx, d, run.ID, core.StatusRunning, "", ""); err == nil {
		t.Fatal("finishing with a status that is not final should fail")
	}
	got, _ = st.GetRun(ctx, d, run.ID)
	if got.Status != core.StatusCompleted || got.State != core.StateCompleted || got.Report != "the report" || got.Error != "" || got.FinishedAt == nil {
		t.Fatalf("finished run: %+v", got)
	}
	states, _ = st.ListStates(ctx, d, run.ID)
	if stateNames(states) != "starting,searching,thinking,completed" || states[2].FinishedAt == nil || states[3].FinishedAt == nil || states[3].Seq != 4 {
		t.Fatalf("states after finish: %+v", states)
	}

	// A failed run puts its error on the state it failed in and on the terminal state.
	failed := &core.Run{UserID: "u1", Topic: "other", State: core.StateStarting}
	if err := st.CreateRun(ctx, d, failed); err != nil {
		t.Fatal(err)
	}
	st.EnterState(ctx, d, failed.ID, core.StateThinking, 1, "step 1")
	if ok, err := st.FinishRun(ctx, d, failed.ID, core.StatusFailed, "", "model down"); err != nil || !ok {
		t.Fatalf("finish failed: %v %v", ok, err)
	}
	states, _ = st.ListStates(ctx, d, failed.ID)
	if stateNames(states) != "thinking,failed" || states[0].Error != "model down" || states[1].Error != "model down" {
		t.Fatalf("failed states: %+v", states)
	}

	// Listing: newest first, by user and by status.
	other := &core.Run{UserID: "u2", Topic: "t"}
	if err := st.CreateRun(ctx, d, other); err != nil {
		t.Fatal(err)
	}
	ids := func(f core.RunFilter, limit int) []int64 {
		runs, err := st.ListRuns(ctx, d, f, limit)
		if err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, r := range runs {
			out = append(out, r.ID)
		}
		return out
	}
	eq := func(got []int64, want ...int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}
	eq(ids(core.RunFilter{}, 0), other.ID, failed.ID, run.ID)
	eq(ids(core.RunFilter{UserID: "u1"}, 0), failed.ID, run.ID)
	eq(ids(core.RunFilter{UserID: "u1"}, 1), failed.ID)
	eq(ids(core.RunFilter{Statuses: []core.Status{core.StatusRunning}}, 0), other.ID)
	eq(ids(core.RunFilter{UserID: "u1", Statuses: []core.Status{core.StatusCompleted, core.StatusFailed}}, 0), failed.ID, run.ID)
	eq(ids(core.RunFilter{UserID: "nobody"}, 0))

	// The runs that were working when the process died are failed.
	if n, err := st.FailActive(ctx, d, "restarted"); err != nil || n != 1 {
		t.Fatalf("fail active: %d %v", n, err)
	}
	if n, _ := st.FailActive(ctx, d, "restarted"); n != 0 {
		t.Fatalf("fail active again: %d", n)
	}
	got, _ = st.GetRun(ctx, d, other.ID)
	if got.Status != core.StatusFailed || got.Error != "restarted" {
		t.Fatalf("interrupted run: %+v", got)
	}

	if _, err := st.GetRun(ctx, d, 999999); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("get unknown: %v", err)
	}
	if states, err := st.ListStates(ctx, d, 999999); err != nil || len(states) != 0 {
		t.Fatalf("states of unknown: %v %v", states, err)
	}
}

func TestEnterStateInATransactionRollsBack(t *testing.T) { runOnBackends(t, testRollback) }

func testRollback(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	run := &core.Run{UserID: "u1", Topic: "t"}
	if err := ops.Store.CreateRun(ctx, d, run); err != nil {
		t.Fatal(err)
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Store.EnterState(ctx, tx, run.ID, core.StateThinking, 1, ""); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if states, _ := ops.Store.ListStates(ctx, d, run.ID); len(states) != 0 {
		t.Fatalf("states: %+v", states)
	}
}
