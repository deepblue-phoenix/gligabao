package relational_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestTrainingJobRuns(t *testing.T) { dbtest.Run(t, trainingJobRunsScenario) }

func trainingJobRunsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	started := relational.Normalize(time.Now().Add(-time.Minute))

	id, err := relational.RecordTrainingJobRun(ctx, d, relational.TrainingJobRun{
		JobID: "fc-1", Status: "started", ModelSize: "12b", NumTrainSteps: 7, StartedAt: started})
	if err != nil || id == 0 {
		t.Fatalf("RecordTrainingJobRun: %d, %v", id, err)
	}
	// Each started job is a new record, even with the same settings.
	id2, err := relational.RecordTrainingJobRun(ctx, d, relational.TrainingJobRun{
		JobID: "fc-2", Status: "started", ModelSize: "12b", NumTrainSteps: 7, StartedAt: started})
	if err != nil || id2 == id {
		t.Fatalf("second record: %d, %v", id2, err)
	}

	got, err := relational.GetTrainingJobRun(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.JobID != "fc-1" || got.Status != "started" || got.ModelSize != "12b" || got.NumTrainSteps != 7 ||
		!got.StartedAt.Equal(started) || got.CreatedAt.IsZero() {
		t.Fatalf("GetTrainingJobRun: %+v", got)
	}

	if _, err := relational.GetTrainingJobRun(ctx, d, 999999); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("missing run: %v", err)
	}
}

func TestTrainingJobEvents(t *testing.T) { dbtest.Run(t, trainingJobEventsScenario) }

func trainingJobEventsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	started := relational.Normalize(time.Now().Add(-time.Minute))
	id, err := relational.RecordTrainingJobRun(ctx, d, relational.TrainingJobRun{
		JobID: "j1", ProviderJobID: "fc-1", Status: "started", ModelSize: "4b", Dataset: "mine.jsonl", NumTrainSteps: 10, StartedAt: started})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := relational.GetTrainingJobRun(ctx, d, id)
	if got.ProviderJobID != "fc-1" || got.Dataset != "mine.jsonl" || got.LastStep != 0 || got.LastLoss != nil || got.FinalLoss != nil ||
		got.FinishedAt != nil || got.Error != "" {
		t.Fatalf("fresh run: %+v", got)
	}

	// Events are stored once per seq, in order.
	if n, err := relational.NextTrainingJobEventSeq(ctx, d, id); err != nil || n != 0 {
		t.Fatalf("next seq of a run without events: %d, %v", n, err)
	}
	for seq, typ := range []string{"status", "progress", "progress"} {
		isNew, err := relational.RecordTrainingJobEvent(ctx, d, id, seq, typ, fmt.Sprintf(`{"seq":%d}`, seq))
		if err != nil || !isNew {
			t.Fatalf("event %d: %v, %v", seq, isNew, err)
		}
	}
	if isNew, err := relational.RecordTrainingJobEvent(ctx, d, id, 1, "progress", `{"other":true}`); err != nil || isNew {
		t.Fatalf("a replayed event must be ignored: %v, %v", isNew, err)
	}
	evs, err := relational.ListTrainingJobEvents(ctx, d, id, 1)
	if err != nil || len(evs) != 2 || evs[0].Seq != 1 || evs[0].Data != `{"seq":1}` || evs[1].Seq != 2 || evs[0].CreatedAt.IsZero() {
		t.Fatalf("events from 1: %+v, %v", evs, err)
	}
	if n, _ := relational.NextTrainingJobEventSeq(ctx, d, id); n != 3 {
		t.Fatalf("next seq: %d", n)
	}

	// Updates write only what is set.
	step, loss, final := 5, 1.25, 0.75
	if err := relational.UpdateTrainingJobRun(ctx, d, id, relational.TrainingJobRunUpdate{Status: "training"}); err != nil {
		t.Fatal(err)
	}
	if err := relational.UpdateTrainingJobRun(ctx, d, id, relational.TrainingJobRunUpdate{Step: &step, Loss: &loss}); err != nil {
		t.Fatal(err)
	}
	got, _ = relational.GetTrainingJobRun(ctx, d, id)
	if got.Status != "training" || got.LastStep != 5 || got.LastLoss == nil || *got.LastLoss != 1.25 || got.FinishedAt != nil {
		t.Fatalf("after progress: %+v", got)
	}
	step2 := 6 // a step without a loss moves the step only
	relational.UpdateTrainingJobRun(ctx, d, id, relational.TrainingJobRunUpdate{Step: &step2})
	finished := relational.Normalize(time.Now())
	if err := relational.UpdateTrainingJobRun(ctx, d, id, relational.TrainingJobRunUpdate{Status: "completed", FinalLoss: &final, FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	got, _ = relational.GetTrainingJobRun(ctx, d, id)
	if got.Status != "completed" || got.LastStep != 6 || *got.LastLoss != 1.25 || got.FinalLoss == nil || *got.FinalLoss != 0.75 ||
		got.FinishedAt == nil || !got.FinishedAt.Equal(finished) || !relational.TrainingJobFinished(got.Status) {
		t.Fatalf("after completion: %+v", got)
	}
	if err := relational.UpdateTrainingJobRun(ctx, d, 999, relational.TrainingJobRunUpdate{Status: "failed", Error: "x"}); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("update of a missing run: %v", err)
	}

	// Listing: newest first; unfinished only, oldest first.
	id2, _ := relational.RecordTrainingJobRun(ctx, d, relational.TrainingJobRun{JobID: "j2", Status: "started", ModelSize: "12b", NumTrainSteps: 3, StartedAt: started})
	id3, _ := relational.RecordTrainingJobRun(ctx, d, relational.TrainingJobRun{JobID: "j3", Status: "started", ModelSize: "12b", NumTrainSteps: 3, StartedAt: started})
	relational.UpdateTrainingJobRun(ctx, d, id3, relational.TrainingJobRunUpdate{Status: "failed", Error: "oom"})
	all, err := relational.ListTrainingJobRuns(ctx, d, 2, false)
	if err != nil || len(all) != 2 || all[0].ID != id3 || all[1].ID != id2 || all[0].Error != "oom" {
		t.Fatalf("list: %+v, %v", all, err)
	}
	open, err := relational.ListTrainingJobRuns(ctx, d, 0, true)
	if err != nil || len(open) != 1 || open[0].ID != id2 {
		t.Fatalf("unfinished: %+v, %v", open, err)
	}
}
