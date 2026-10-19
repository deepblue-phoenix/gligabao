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

func TestTrainedModelSuiteRuns(t *testing.T) { dbtest.Run(t, trainedModelSuiteRunsScenario) }

func trainedModelSuiteRunsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	mid, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{
		JobID: "jobeval00001", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 5, Path: "trained/jobeval00001"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := relational.RecordTrainedModelSuiteRun(ctx, d, relational.TrainedModelSuiteRun{
		TrainedModelID: mid, Dataset: "mine", DatasetSHA256: "abc", NumExamples: 3, Params: json.RawMessage(`{"batch_size":2}`)})
	if err != nil || id == 0 {
		t.Fatalf("RecordTrainedModelSuiteRun: %d %v", id, err)
	}
	got, err := relational.GetTrainedModelSuiteRun(ctx, d, id)
	if err != nil || got.Status != relational.TrainedModelSuiteRunRunning || got.TrainedModelID != mid || got.Dataset != "mine" ||
		got.NumExamples != 3 || string(got.Params) != `{"batch_size":2}` || got.CreatedAt.IsZero() || got.FinishedAt != nil {
		t.Fatalf("GetTrainedModelSuiteRun: %+v %v", got, err)
	}

	// A batch: its cases and the counters after it.
	score := 5
	e := *got
	e.JudgeModel, e.NumAnswered, e.NumJudged, e.NumFailed, e.NumCorrect, e.ScoreSum = "judge-1", 2, 1, 1, 1, 5
	e.NumBatches, e.JudgeCalls, e.JudgeInputTokens, e.JudgeOutputTokens, e.JudgeCostUSD = 1, 1, 100, 10, 0.25
	cases := []relational.TrainedModelCaseResult{
		{Idx: 0, Batch: 1, Instruction: "q0", Input: "ctx", Reference: "a0", Response: "a0", Status: relational.TrainedModelCaseJudged,
			Score: &score, Reason: "same", InputTokens: 3, OutputTokens: 2, DurationMS: 40},
		{Idx: 1, Batch: 1, Instruction: "q1", Reference: "a1", Status: relational.TrainedModelCaseFailed, Error: "model: boom"},
	}
	if err := relational.SaveTrainedModelSuiteRunBatch(ctx, d, e, cases); err != nil {
		t.Fatal(err)
	}
	got, err = relational.GetTrainedModelSuiteRun(ctx, d, id)
	if err != nil || got.JudgeModel != "judge-1" || got.NumJudged != 1 || got.NumFailed != 1 || got.NumAnswered != 2 || got.NumCorrect != 1 ||
		got.ScoreSum != 5 || got.NumBatches != 1 || got.JudgeCalls != 1 || got.JudgeInputTokens != 100 || got.JudgeOutputTokens != 10 || got.JudgeCostUSD != 0.25 {
		t.Fatalf("after a batch: %+v %v", got, err)
	}
	list, err := relational.ListTrainedModelCaseResults(ctx, d, id)
	if err != nil || len(list) != 2 {
		t.Fatalf("cases: %+v %v", list, err)
	}
	if a := list[0]; a.Idx != 0 || a.Score == nil || *a.Score != 5 || a.Reason != "same" || a.Input != "ctx" || a.Response != "a0" || a.InputTokens != 3 || a.DurationMS != 40 || a.SuiteRunID != id {
		t.Fatalf("case 0: %+v", a)
	}
	if b := list[1]; b.Score != nil || b.Status != relational.TrainedModelCaseFailed || b.Error != "model: boom" {
		t.Fatalf("case 1: %+v", b)
	}
	// The same example twice is refused, and nothing of that batch is kept.
	if err := relational.SaveTrainedModelSuiteRunBatch(ctx, d, e, cases[:1]); err == nil {
		t.Fatal("a duplicate case was accepted")
	}
	if err := relational.SaveTrainedModelSuiteRunBatch(ctx, d, relational.TrainedModelSuiteRun{ID: id + 100}, nil); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("unknown suite run: %v", err)
	}

	// Finishing: once. A second finish leaves the first outcome.
	if err := relational.FinishTrainedModelSuiteRun(ctx, d, id, relational.TrainedModelSuiteRunCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if err := relational.FinishTrainedModelSuiteRun(ctx, d, id, relational.TrainedModelSuiteRunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = relational.GetTrainedModelSuiteRun(ctx, d, id)
	if got.Status != relational.TrainedModelSuiteRunCancelled || got.FinishedAt == nil {
		t.Fatalf("finished: %+v", got)
	}
	if err := relational.FinishTrainedModelSuiteRun(ctx, d, id+100, relational.TrainedModelSuiteRunFailed, "x"); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("finish unknown: %v", err)
	}

	// A restart fails what was running, and only that.
	id2, err := relational.RecordTrainedModelSuiteRun(ctx, d, relational.TrainedModelSuiteRun{TrainedModelID: mid, Dataset: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := relational.FailRunningTrainedModelSuiteRuns(ctx, d, "restarted")
	if err != nil || n != 1 {
		t.Fatalf("FailRunningTrainedModelSuiteRuns: %d %v", n, err)
	}
	got2, _ := relational.GetTrainedModelSuiteRun(ctx, d, id2)
	if got2.Status != relational.TrainedModelSuiteRunFailed || got2.Error != "restarted" || got2.FinishedAt == nil {
		t.Fatalf("interrupted: %+v", got2)
	}
	if got, _ = relational.GetTrainedModelSuiteRun(ctx, d, id); got.Status != relational.TrainedModelSuiteRunCancelled {
		t.Fatalf("a finished suite run changed: %+v", got)
	}

	all, err := relational.ListTrainedModelSuiteRuns(ctx, d, mid, 0)
	if err != nil || len(all) != 2 || all[0].ID != id2 {
		t.Fatalf("list: %+v %v", all, err)
	}
	if one, _ := relational.ListTrainedModelSuiteRuns(ctx, d, mid, 1); len(one) != 1 {
		t.Fatalf("limit: %+v", one)
	}
	if none, _ := relational.ListTrainedModelSuiteRuns(ctx, d, mid+100, 0); len(none) != 0 {
		t.Fatalf("other model: %+v", none)
	}
	if _, err := relational.GetTrainedModelSuiteRun(ctx, d, id+100); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}
