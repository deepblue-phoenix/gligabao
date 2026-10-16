package relational_test

import (
	"context"
	"database/sql"
	"errors"
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
