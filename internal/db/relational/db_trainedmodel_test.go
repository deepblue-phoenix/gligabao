package relational_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestTrainedModels(t *testing.T) { dbtest.Run(t, trainedModelsScenario) }

func trainedModelsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	runID, err := relational.RecordTrainingJobRun(ctx, d, relational.TrainingJobRun{
		JobID: "job1", Status: "started", ModelSize: "4b", NumTrainSteps: 50, StartedAt: relational.Normalize(time.Now())})
	if err != nil {
		t.Fatal(err)
	}
	loss := 0.5
	m := relational.TrainedModel{
		RunID: &runID, JobID: "job1aaaaaaaa", BaseModel: "google/gemma-4-E4B-it", ModelSize: "4b", Dataset: "mine.jsonl",
		DatasetSHA256: "abc", NumExamples: 12, NumTrainSteps: 50, FinalLoss: &loss, Path: "trained/job1aaaaaaaa", SizeBytes: 15,
		Params: json.RawMessage(`{"lora_rank":16}`),
		Files:  []relational.TrainedModelFile{{Path: "adapter_config.json", SizeBytes: 5, SHA256: "h1"}, {Path: "w.safetensors", SizeBytes: 10}},
	}
	id, isNew, err := relational.RecordTrainedModel(ctx, d, m)
	if err != nil || !isNew || id == 0 {
		t.Fatalf("RecordTrainedModel: %d %v %v", id, isNew, err)
	}
	// The same job again (a replayed event) leaves the record as it is.
	again := m
	again.Name = "other"
	if id2, isNew, err := relational.RecordTrainedModel(ctx, d, again); err != nil || isNew || id2 != id {
		t.Fatalf("same job again: %d %v %v", id2, isNew, err)
	}

	got, err := relational.GetTrainedModel(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "4b-mine.jsonl-50steps-job1aaaa" || got.RunID == nil || *got.RunID != runID || got.JobID != m.JobID ||
		got.BaseModel != m.BaseModel || got.ModelSize != "4b" || got.Dataset != "mine.jsonl" || got.DatasetSHA256 != "abc" ||
		got.NumExamples != 12 || got.NumTrainSteps != 50 || got.FinalLoss == nil || *got.FinalLoss != 0.5 ||
		got.Path != m.Path || got.SizeBytes != 15 || got.CreatedAt.IsZero() {
		t.Fatalf("GetTrainedModel: %+v", got)
	}
	if string(got.Params) != `{"lora_rank":16}` || len(got.Files) != 2 || got.Files[0] != m.Files[0] || got.Files[1] != m.Files[1] {
		t.Fatalf("params/files: %s %+v", got.Params, got.Files)
	}

	// Training again, even with the same settings, is a new model.
	m2 := m
	m2.JobID, m2.Path, m2.RunID, m2.FinalLoss, m2.Params, m2.Files, m2.Dataset = "job2bbbbbbbb", "trained/job2bbbbbbbb", nil, nil, nil, nil, ""
	id2, isNew, err := relational.RecordTrainedModel(ctx, d, m2)
	if err != nil || !isNew || id2 == id {
		t.Fatalf("second model: %d %v %v", id2, isNew, err)
	}
	got2, _ := relational.GetTrainedModel(ctx, d, id2)
	if got2.RunID != nil || got2.FinalLoss != nil || string(got2.Params) != "{}" || got2.Files == nil || len(got2.Files) != 0 ||
		got2.Name != "4b-default-50steps-job2bbbb" {
		t.Fatalf("model without optional fields: %+v", got2)
	}

	list, err := relational.ListTrainedModels(ctx, d, 0)
	if err != nil || len(list) != 2 || list[0].ID != id2 || list[1].ID != id {
		t.Fatalf("ListTrainedModels: %+v %v", list, err)
	}
	if list, _ := relational.ListTrainedModels(ctx, d, 1); len(list) != 1 || list[0].ID != id2 {
		t.Fatalf("limit: %+v", list)
	}
	if _, err := relational.GetTrainedModel(ctx, d, 999999); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("missing model: %v", err)
	}
}

func TestInferenceRequests(t *testing.T) { dbtest.Run(t, inferenceRequestsScenario) }

func inferenceRequestsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	var modelIDs []int64
	for _, job := range []string{"jobAAAAAAAA", "jobBBBBBBBB"} {
		id, _, err := relational.RecordTrainedModel(ctx, d, relational.TrainedModel{JobID: job, ModelSize: "4b", NumTrainSteps: 1, Path: "trained/" + job})
		if err != nil {
			t.Fatal(err)
		}
		modelIDs = append(modelIDs, id)
	}
	temp := 0.3
	id1, err := relational.RecordInferenceRequest(ctx, d, relational.InferenceRequest{TrainedModelID: modelIDs[0], Prompt: "p1",
		ModelSize: "4b", ModelPath: "trained/jobAAAAAAAA", MaxTokens: 64, Temperature: &temp})
	if err != nil || id1 == 0 {
		t.Fatalf("RecordInferenceRequest: %d %v", id1, err)
	}
	got, err := relational.GetInferenceRequest(ctx, d, id1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != relational.InferenceRunning || got.Prompt != "p1" || got.TrainedModelID != modelIDs[0] || got.MaxTokens != 64 ||
		got.Temperature == nil || *got.Temperature != 0.3 || got.CompletedAt != nil || got.CreatedAt.IsZero() || got.ModelPath != "trained/jobAAAAAAAA" {
		t.Fatalf("running request: %+v", got)
	}
	if err := relational.FinishInferenceRequest(ctx, d, id1, relational.InferenceCompleted, "reply", "", 1234); err != nil {
		t.Fatal(err)
	}
	id2, err := relational.RecordInferenceRequest(ctx, d, relational.InferenceRequest{TrainedModelID: modelIDs[0], Prompt: "p2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := relational.FinishInferenceRequest(ctx, d, id2, relational.InferenceFailed, "", "boom", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := relational.RecordInferenceRequest(ctx, d, relational.InferenceRequest{TrainedModelID: modelIDs[1], Prompt: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := relational.FinishInferenceRequest(ctx, d, 999, relational.InferenceFailed, "", "", 0); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("finish unknown: %v", err)
	}

	got, err = relational.GetInferenceRequest(ctx, d, id1)
	if err != nil || got.Status != relational.InferenceCompleted || got.Response != "reply" || got.DurationMS != 1234 || got.CompletedAt == nil {
		t.Fatalf("completed request: %+v %v", got, err)
	}
	list, err := relational.ListInferenceRequests(ctx, d, modelIDs[0], 0)
	if err != nil || len(list) != 2 || list[0].ID != id2 || list[0].Status != relational.InferenceFailed || list[0].Error != "boom" || list[1].ID != id1 {
		t.Fatalf("list of model 1: %+v %v", list, err)
	}
	if list, err = relational.ListInferenceRequests(ctx, d, 0, 2); err != nil || len(list) != 2 {
		t.Fatalf("list of all, limit 2: %+v %v", list, err)
	}
	if _, err := relational.GetInferenceRequest(ctx, d, 999); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("get unknown: %v", err)
	}
}
