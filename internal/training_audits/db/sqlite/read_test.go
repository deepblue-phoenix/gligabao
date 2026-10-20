package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db/sqlite"
)

func TestGetListUpdateFinish(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	s := sqlite.Store

	started := time.Now()
	first := &core.AuditRun{TrainedModelID: 3, Dataset: "mine", DatasetSHA256: "abc", Params: core.Params{"batch_size": 10.0},
		Status: core.StatusRunning, Counts: core.Counts{Total: 3, Pending: 3}, Timing: core.Timing{StartedAt: &started}}
	second := &core.AuditRun{TrainedModelID: 4, Dataset: "other", Status: core.StatusRunning}
	third := &core.AuditRun{TrainedModelID: 3, Dataset: "mine"}
	for _, r := range []*core.AuditRun{first, second, third} {
		if err := s.CreateAuditRun(ctx, d, r); err != nil {
			t.Fatal(err)
		}
	}

	// Get returns what was created.
	got, err := s.GetAuditRun(ctx, d, first.ID)
	if err != nil || got.ID != first.ID || got.TrainedModelID != 3 || got.Dataset != "mine" || got.DatasetSHA256 != "abc" || got.Status != core.StatusRunning ||
		got.Params["batch_size"] != 10.0 || got.Counts != first.Counts || got.StartedAt == nil || !got.StartedAt.Equal(started.UTC().Truncate(time.Second)) ||
		got.FinishedAt != nil || !got.CreatedAt.Equal(first.CreatedAt) || got.CaseRuns != nil {
		t.Fatalf("GetAuditRun: %+v %v", got, err)
	}
	if _, err := s.GetAuditRun(ctx, d, 999); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetAuditRun of an unknown run: %v", err)
	}

	// List: newest first, of one model or of all, limited.
	list, err := s.ListAuditRuns(ctx, d, 3, 0)
	if err != nil || len(list) != 2 || list[0].ID != third.ID || list[1].ID != first.ID {
		t.Fatalf("ListAuditRuns of a model: %+v %v", list, err)
	}
	if list, err = s.ListAuditRuns(ctx, d, 0, 2); err != nil || len(list) != 2 || list[0].ID != third.ID || list[1].ID != second.ID {
		t.Fatalf("ListAuditRuns limited: %+v %v", list, err)
	}
	if list, err = s.ListAuditRuns(ctx, d, 99, 0); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("ListAuditRuns of an unknown model: %+v %v", list, err)
	}

	// Update writes what changes while it runs, and only that.
	upd := *got
	upd.Counts = core.Counts{Total: 3, Pending: 1, Completed: 1, Errored: 1}
	upd.AuditStats = core.AuditStats{JudgeModel: "j", NumAnswered: 2, NumJudged: 1, NumCorrect: 1, ScoreSum: 5, NumBatches: 1, JudgeCalls: 1,
		JudgeInputTokens: 100, JudgeOutputTokens: 20}
	upd.CostUSD = 0.25
	upd.Dataset, upd.Params = "changed", core.Params{"x": 1}
	if err := s.UpdateAuditRun(ctx, d, &upd); err != nil {
		t.Fatalf("UpdateAuditRun: %v", err)
	}
	got, _ = s.GetAuditRun(ctx, d, first.ID)
	if got.Counts != upd.Counts || got.AuditStats != upd.AuditStats || got.CostUSD != 0.25 || got.Dataset != "mine" || got.Params["batch_size"] != 10.0 {
		t.Fatalf("after UpdateAuditRun: %+v", got)
	}
	if err := s.UpdateAuditRun(ctx, d, &core.AuditRun{ID: 999}); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("UpdateAuditRun of an unknown run: %v", err)
	}
	bad := upd
	bad.NumCorrect = 0
	if err := s.UpdateAuditRun(ctx, d, &bad); err == nil {
		t.Error("UpdateAuditRun accepted statistics that do not add up")
	}

	// Case runs: in dataset order, append-only.
	score := 4
	for _, c := range []*core.AuditCaseRun{
		{RunID: first.ID, Idx: 1, Status: core.StatusErrored, Error: "model: x", Instruction: "q1"},
		{RunID: first.ID, Idx: 0, Status: core.StatusCompleted, Instruction: "q0", Input: "ctx", Reference: "a0", Response: "a0", Batch: 1,
			JudgeScore: &score, JudgeReason: "ok", InputTokens: 5, OutputTokens: 3, DurationMS: 120},
	} {
		if err := s.CreateAuditCaseRun(ctx, d, c); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := s.ListAuditCaseRuns(ctx, d, first.ID)
	if err != nil || len(cs) != 2 || cs[0].Idx != 0 || cs[1].Idx != 1 {
		t.Fatalf("ListAuditCaseRuns: %+v %v", cs, err)
	}
	if c := cs[0]; c.ID == 0 || c.RunID != first.ID || c.Status != core.StatusCompleted || c.Input != "ctx" || c.Reference != "a0" || c.Response != "a0" ||
		c.Batch != 1 || c.JudgeScore == nil || *c.JudgeScore != 4 || c.JudgeReason != "ok" || c.InputTokens != 5 || c.OutputTokens != 3 || c.DurationMS != 120 || c.CreatedAt.IsZero() {
		t.Errorf("case run 0: %+v", c)
	}
	if c := cs[1]; c.Status != core.StatusErrored || c.Error != "model: x" || c.JudgeScore != nil {
		t.Errorf("case run 1: %+v", c)
	}
	if cs, err = s.ListAuditCaseRuns(ctx, d, second.ID); err != nil || cs == nil || len(cs) != 0 {
		t.Errorf("ListAuditCaseRuns of a run without any: %+v %v", cs, err)
	}

	// Finish ends a run that is going, once; Fail ends all that are going.
	if err := s.FinishAuditRun(ctx, d, first.ID, core.StatusCancelled, ""); err != nil {
		t.Fatalf("FinishAuditRun: %v", err)
	}
	got, _ = s.GetAuditRun(ctx, d, first.ID)
	if got.Status != core.StatusCancelled || got.FinishedAt == nil || got.Counts != upd.Counts {
		t.Fatalf("after FinishAuditRun: %+v", got)
	}
	if err := s.FinishAuditRun(ctx, d, first.ID, core.StatusErrored, "late"); err != nil {
		t.Fatalf("FinishAuditRun of a finished run: %v", err)
	}
	if got, _ = s.GetAuditRun(ctx, d, first.ID); got.Status != core.StatusCancelled || got.Error != "" {
		t.Errorf("a finished run was ended again: %+v", got)
	}
	if err := s.FinishAuditRun(ctx, d, 999, core.StatusErrored, "x"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("FinishAuditRun of an unknown run: %v", err)
	}
	if err := s.FinishAuditRun(ctx, d, second.ID, core.StatusRunning, ""); err == nil {
		t.Error("FinishAuditRun accepted a status that is not final")
	}
	n, err := s.FailRunningAuditRuns(ctx, d, "restarted")
	if err != nil || n != 2 { // second (running) and third (pending)
		t.Fatalf("FailRunningAuditRuns: %d %v", n, err)
	}
	for _, id := range []core.AuditRunID{second.ID, third.ID} {
		if got, _ = s.GetAuditRun(ctx, d, id); got.Status != core.StatusErrored || got.Error != "restarted" || got.FinishedAt == nil {
			t.Errorf("run %d after FailRunningAuditRuns: %+v", id, got)
		}
	}
	if got, _ = s.GetAuditRun(ctx, d, first.ID); got.Status != core.StatusCancelled {
		t.Errorf("a finished run was failed: %+v", got)
	}
	if n, err = s.FailRunningAuditRuns(ctx, d, "again"); err != nil || n != 0 {
		t.Errorf("FailRunningAuditRuns with none running: %d %v", n, err)
	}

	// The functions work in a transaction.
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &core.AuditRun{TrainedModelID: 8, Dataset: "tmp"}
	if err := s.CreateAuditRun(ctx, tx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAuditRun(ctx, tx, r.ID); err != nil {
		t.Errorf("GetAuditRun in its transaction: %v", err)
	}
	tx.Rollback()
	if _, err := s.GetAuditRun(ctx, d, r.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("rolled back run: %v", err)
	}
}
