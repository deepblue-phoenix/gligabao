package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

func TestTrainingJobRunMeta(t *testing.T) { dbtest.Run(t, trainingJobRunMetaScenario) }

func trainingJobRunMetaScenario(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	id, err := trainingdb.RecordTrainingJobRun(ctx,
		d,
		trainingdb.TrainingJobRun{
			UserID:        "u1",
			ModelType:     trainingdb.ModelTypeGCNII,
			JobID:         "meta-1",
			Status:        "running",
			NumTrainSteps: 5,
			StartedAt:     relational.Normalize(time.Now()),
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trainingdb.GetTrainingJobRunMeta(ctx,
		d,
		id); !errors.Is(err, trainingdb.ErrNotFound) {
		t.Fatalf("meta of a run without one: %v", err)
	}

	epochs, lr, seed, valLoss := 5, 0.01, int64(-7), 0.5
	m := trainingdb.TrainingJobRunMeta{
		RunID:      id,
		UserID:     "u1",
		ModelType:  trainingdb.ModelTypeGCNII,
		Epochs:     &epochs,
		LR:         &lr,
		Seed:       &seed,
		MetaSchema: "gcnii.v1",
		Meta:       json.RawMessage(`{"hidden":16}`),
	}
	if err := trainingdb.UpsertTrainingJobRunMeta(ctx,
		d,
		m); err != nil {
		t.Fatal(err)
	}
	got, err := trainingdb.GetTrainingJobRunMeta(ctx,
		d,
		id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Epochs == nil || *got.Epochs != 5 || got.LR == nil || *got.LR != 0.01 || got.Seed == nil || *got.Seed != -7 ||
		got.BatchSize != nil || got.FinalValLoss != nil || got.MetaSchema != "gcnii.v1" || string(got.Meta) != `{"hidden":16}` ||
		got.CreatedAt.IsZero() {
		t.Fatalf("meta: %+v", got)
	}

	// a second upsert replaces the row (the run ending adds the results)
	m.FinalValLoss, m.Meta = &valLoss, nil
	if err := trainingdb.UpsertTrainingJobRunMeta(ctx,
		d,
		m); err != nil {
		t.Fatal(err)
	}
	got, err = trainingdb.GetTrainingJobRunMeta(ctx,
		d,
		id)
	if err != nil || got.FinalValLoss == nil || *got.FinalValLoss != 0.5 || string(got.Meta) != "{}" {
		t.Fatalf("replaced meta: %+v, %v", got, err)
	}
}
