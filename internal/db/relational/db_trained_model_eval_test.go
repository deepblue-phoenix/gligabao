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

func TestTrainedModelEvals(t *testing.T) { dbtest.Run(t, trainedModelEvalsScenario) }

func trainedModelEvalsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	mid, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{
		JobID: "jobeval00001", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 5, Path: "trained/jobeval00001"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := relational.RecordTrainedModelEval(ctx, d, relational.TrainedModelEval{
		TrainedModelID: mid, Dataset: "mine", DatasetSHA256: "abc", NumExamples: 3, Params: json.RawMessage(`{"batch_size":2}`)})
	if err != nil || id == 0 {
		t.Fatalf("RecordTrainedModelEval: %d %v", id, err)
	}
	got, err := relational.GetTrainedModelEval(ctx, d, id)
	if err != nil || got.Status != relational.TrainedModelEvalRunning || got.TrainedModelID != mid || got.Dataset != "mine" ||
		got.NumExamples != 3 || string(got.Params) != `{"batch_size":2}` || got.CreatedAt.IsZero() || got.FinishedAt != nil {
		t.Fatalf("GetTrainedModelEval: %+v %v", got, err)
	}

	// A batch: its items and the counters after it.
	score := 5
	e := *got
	e.JudgeModel, e.NumAnswered, e.NumJudged, e.NumFailed, e.NumCorrect, e.ScoreSum = "judge-1", 2, 1, 1, 1, 5
	e.NumBatches, e.JudgeCalls, e.JudgeInputTokens, e.JudgeOutputTokens, e.JudgeCostUSD = 1, 1, 100, 10, 0.25
	items := []relational.TrainedModelEvalItem{
		{Idx: 0, Batch: 1, Instruction: "q0", Input: "ctx", Reference: "a0", Response: "a0", Status: relational.TrainedModelEvalItemJudged,
			Score: &score, Reason: "same", InputTokens: 3, OutputTokens: 2, DurationMS: 40},
		{Idx: 1, Batch: 1, Instruction: "q1", Reference: "a1", Status: relational.TrainedModelEvalItemFailed, Error: "model: boom"},
	}
	if err := relational.SaveTrainedModelEvalBatch(ctx, d, e, items); err != nil {
		t.Fatal(err)
	}
	got, err = relational.GetTrainedModelEval(ctx, d, id)
	if err != nil || got.JudgeModel != "judge-1" || got.NumJudged != 1 || got.NumFailed != 1 || got.NumAnswered != 2 || got.NumCorrect != 1 ||
		got.ScoreSum != 5 || got.NumBatches != 1 || got.JudgeCalls != 1 || got.JudgeInputTokens != 100 || got.JudgeOutputTokens != 10 || got.JudgeCostUSD != 0.25 {
		t.Fatalf("after a batch: %+v %v", got, err)
	}
	list, err := relational.ListTrainedModelEvalItems(ctx, d, id)
	if err != nil || len(list) != 2 {
		t.Fatalf("items: %+v %v", list, err)
	}
	if a := list[0]; a.Idx != 0 || a.Score == nil || *a.Score != 5 || a.Reason != "same" || a.Input != "ctx" || a.Response != "a0" || a.InputTokens != 3 || a.DurationMS != 40 || a.EvalID != id {
		t.Fatalf("item 0: %+v", a)
	}
	if b := list[1]; b.Score != nil || b.Status != relational.TrainedModelEvalItemFailed || b.Error != "model: boom" {
		t.Fatalf("item 1: %+v", b)
	}
	// The same example twice is refused, and nothing of that batch is kept.
	if err := relational.SaveTrainedModelEvalBatch(ctx, d, e, items[:1]); err == nil {
		t.Fatal("a duplicate item was accepted")
	}
	if err := relational.SaveTrainedModelEvalBatch(ctx, d, relational.TrainedModelEval{ID: id + 100}, nil); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("unknown eval: %v", err)
	}

	// Finishing: once. A second finish leaves the first outcome.
	if err := relational.FinishTrainedModelEval(ctx, d, id, relational.TrainedModelEvalCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if err := relational.FinishTrainedModelEval(ctx, d, id, relational.TrainedModelEvalCompleted, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = relational.GetTrainedModelEval(ctx, d, id)
	if got.Status != relational.TrainedModelEvalCancelled || got.FinishedAt == nil {
		t.Fatalf("finished: %+v", got)
	}
	if err := relational.FinishTrainedModelEval(ctx, d, id+100, relational.TrainedModelEvalFailed, "x"); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("finish unknown: %v", err)
	}

	// A restart fails what was running, and only that.
	id2, err := relational.RecordTrainedModelEval(ctx, d, relational.TrainedModelEval{TrainedModelID: mid, Dataset: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := relational.FailRunningTrainedModelEvals(ctx, d, "restarted")
	if err != nil || n != 1 {
		t.Fatalf("FailRunningTrainedModelEvals: %d %v", n, err)
	}
	got2, _ := relational.GetTrainedModelEval(ctx, d, id2)
	if got2.Status != relational.TrainedModelEvalFailed || got2.Error != "restarted" || got2.FinishedAt == nil {
		t.Fatalf("interrupted: %+v", got2)
	}
	if got, _ = relational.GetTrainedModelEval(ctx, d, id); got.Status != relational.TrainedModelEvalCancelled {
		t.Fatalf("a finished eval changed: %+v", got)
	}

	all, err := relational.ListTrainedModelEvals(ctx, d, mid, 0)
	if err != nil || len(all) != 2 || all[0].ID != id2 {
		t.Fatalf("list: %+v %v", all, err)
	}
	if one, _ := relational.ListTrainedModelEvals(ctx, d, mid, 1); len(one) != 1 {
		t.Fatalf("limit: %+v", one)
	}
	if none, _ := relational.ListTrainedModelEvals(ctx, d, mid+100, 0); len(none) != 0 {
		t.Fatalf("other model: %+v", none)
	}
	if _, err := relational.GetTrainedModelEval(ctx, d, id+100); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}
