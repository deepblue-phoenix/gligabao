package relational_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestTrainedModelAuditRuns(t *testing.T) { dbtest.Run(t, trainedModelAuditRunsScenario) }

func trainedModelAuditRunsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	mid, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{
		JobID: "jobaudit0001", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 5, Path: "trained/jobaudit0001"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := relational.RecordTrainedModelAuditRun(ctx, d, relational.TrainedModelAuditRun{
		TrainedModelID: mid, Dataset: "mine", DatasetSHA256: "abc", NumExamples: 3, Params: json.RawMessage(`{"batch_size":2}`)})
	if err != nil || id == 0 {
		t.Fatalf("RecordTrainedModelAuditRun: %d %v", id, err)
	}
	got, err := relational.GetTrainedModelAuditRun(ctx, d, id)
	if err != nil || got.Status != relational.TrainedModelRunRunning || got.TrainedModelID != mid || got.Dataset != "mine" ||
		got.NumExamples != 3 || compactJSON(got.Params) != `{"batch_size":2}` || got.CreatedAt.IsZero() || got.FinishedAt != nil {
		t.Fatalf("GetTrainedModelAuditRun: %+v %v", got, err)
	}

	// A batch: its cases and the counters after it.
	score := 5
	a := *got
	a.JudgeModel, a.NumAnswered, a.NumJudged, a.NumErrored, a.NumCorrect, a.ScoreSum = "judge-1", 2, 1, 1, 1, 5
	a.NumBatches, a.JudgeCalls, a.JudgeInputTokens, a.JudgeOutputTokens, a.JudgeCostUSD = 1, 1, 100, 10, 0.25
	cases := []relational.TrainedModelAuditCase{
		{Idx: 0, Batch: 1, Instruction: "q0", Input: "ctx", Reference: "a0", Response: "a0", Status: relational.TrainedModelCaseJudged,
			Score: &score, Reason: "same", InputTokens: 3, OutputTokens: 2, DurationMS: 40},
		{Idx: 1, Batch: 1, Instruction: "q1", Reference: "a1", Status: relational.TrainedModelCaseErrored, Error: "model: boom"},
	}
	if err := relational.SaveTrainedModelAuditRunBatch(ctx, d, a, cases); err != nil {
		t.Fatal(err)
	}
	got, err = relational.GetTrainedModelAuditRun(ctx, d, id)
	if err != nil || got.JudgeModel != "judge-1" || got.NumJudged != 1 || got.NumErrored != 1 || got.NumAnswered != 2 || got.NumCorrect != 1 ||
		got.ScoreSum != 5 || got.NumBatches != 1 || got.JudgeCalls != 1 || got.JudgeInputTokens != 100 || got.JudgeOutputTokens != 10 || got.JudgeCostUSD != 0.25 {
		t.Fatalf("after a batch: %+v %v", got, err)
	}
	list, err := relational.ListTrainedModelAuditCases(ctx, d, id)
	if err != nil || len(list) != 2 {
		t.Fatalf("cases: %+v %v", list, err)
	}
	if c := list[0]; c.Idx != 0 || c.Score == nil || *c.Score != 5 || c.Reason != "same" || c.Input != "ctx" || c.Response != "a0" ||
		c.InputTokens != 3 || c.DurationMS != 40 || c.AuditRunID != id || c.Status != relational.TrainedModelCaseJudged {
		t.Fatalf("case 0: %+v", c)
	}
	if c := list[1]; c.Score != nil || c.Status != relational.TrainedModelCaseErrored || c.Error != "model: boom" {
		t.Fatalf("case 1: %+v", c)
	}
	// The same example twice is refused, and nothing of that batch is kept.
	if err := relational.SaveTrainedModelAuditRunBatch(ctx, d, a, cases[:1]); err == nil {
		t.Fatal("a duplicate case was accepted")
	}
	if err := relational.SaveTrainedModelAuditRunBatch(ctx, d, relational.TrainedModelAuditRun{ID: id + 100}, nil); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("unknown audit run: %v", err)
	}

	// Finishing: once. A second finish leaves the first outcome.
	if err := relational.FinishTrainedModelAuditRun(ctx, d, id, relational.TrainedModelRunCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if err := relational.FinishTrainedModelAuditRun(ctx, d, id, relational.TrainedModelRunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = relational.GetTrainedModelAuditRun(ctx, d, id)
	if got.Status != relational.TrainedModelRunCancelled || got.FinishedAt == nil {
		t.Fatalf("finished: %+v", got)
	}
	if err := relational.FinishTrainedModelAuditRun(ctx, d, id+100, relational.TrainedModelRunErrored, "x"); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("finish unknown: %v", err)
	}

	// A restart fails what was running, and only that.
	id2, err := relational.RecordTrainedModelAuditRun(ctx, d, relational.TrainedModelAuditRun{TrainedModelID: mid, Dataset: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := relational.FailRunningTrainedModelAuditRuns(ctx, d, "restarted")
	if err != nil || n != 1 {
		t.Fatalf("FailRunningTrainedModelAuditRuns: %d %v", n, err)
	}
	got2, _ := relational.GetTrainedModelAuditRun(ctx, d, id2)
	if got2.Status != relational.TrainedModelRunErrored || got2.Error != "restarted" || got2.FinishedAt == nil {
		t.Fatalf("interrupted: %+v", got2)
	}
	if got, _ = relational.GetTrainedModelAuditRun(ctx, d, id); got.Status != relational.TrainedModelRunCancelled {
		t.Fatalf("a finished audit run changed: %+v", got)
	}

	all, err := relational.ListTrainedModelAuditRuns(ctx, d, mid, 0)
	if err != nil || len(all) != 2 || all[0].ID != id2 {
		t.Fatalf("list: %+v %v", all, err)
	}
	if one, _ := relational.ListTrainedModelAuditRuns(ctx, d, mid, 1); len(one) != 1 {
		t.Fatalf("limit: %+v", one)
	}
	if none, _ := relational.ListTrainedModelAuditRuns(ctx, d, mid+100, 0); len(none) != 0 {
		t.Fatalf("other model: %+v", none)
	}
	if _, err := relational.GetTrainedModelAuditRun(ctx, d, id+100); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}

	// Audits and evals are separate: an audit is not an eval run.
	if evs, err := relational.ListTrainedModelEvalRuns(ctx, d, mid, 0); err != nil || len(evs) != 0 {
		t.Fatalf("an audit shows up as an eval: %+v %v", evs, err)
	}
}
