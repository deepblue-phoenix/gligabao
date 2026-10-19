package db_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

func TestTrainingDatasets(t *testing.T) { dbtest.Run(t, trainingDatasetsScenario) }

func trainingDatasetsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()

	id, err := trainingdb.SaveTrainingDataset(ctx, d, trainingdb.TrainingDataset{Filename: "a.jsonl", SizeBytes: 10, NumRecords: 1, SHA256: "h1"})
	if err != nil || id == 0 {
		t.Fatalf("SaveTrainingDataset: %d, %v", id, err)
	}
	got, err := trainingdb.GetTrainingDatasetByFilename(ctx, d, "a.jsonl")
	if err != nil || got.ID != id || got.SizeBytes != 10 || got.NumRecords != 1 || got.SHA256 != "h1" ||
		got.Status != trainingdb.TrainingDatasetStored || got.SyncedAt != nil || got.CreatedAt.IsZero() {
		t.Fatalf("GetTrainingDatasetByFilename: %+v, %v", got, err)
	}

	if ok, err := trainingdb.MarkTrainingDatasetSynced(ctx, d, id, "h1"); err != nil || !ok {
		t.Fatalf("mark synced: %v, %v", ok, err)
	}
	if got, _ = trainingdb.GetTrainingDatasetByFilename(ctx, d, "a.jsonl"); got.Status != "synced" || got.SyncedAt == nil {
		t.Fatalf("synced: %+v", got)
	}

	// The same filename replaces the record (same ID) and is not synced any more.
	id2, err := trainingdb.SaveTrainingDataset(ctx, d, trainingdb.TrainingDataset{Filename: "a.jsonl", SizeBytes: 6, NumRecords: 2, SHA256: "h2"})
	if err != nil || id2 != id {
		t.Fatalf("re-upload: %d (want %d), %v", id2, id, err)
	}
	if got, _ = trainingdb.GetTrainingDatasetByFilename(ctx, d, "a.jsonl"); got.NumRecords != 2 || got.SHA256 != "h2" || got.Status != "stored" || got.SyncedAt != nil || got.SizeBytes != 6 {
		t.Fatalf("replaced: %+v", got)
	}
	// Syncing the old version (uploaded again meanwhile) must not mark the new one.
	if ok, err := trainingdb.MarkTrainingDatasetSynced(ctx, d, id, "h1"); err != nil || ok {
		t.Fatalf("stale sync: %v, %v", ok, err)
	}
	if got, _ = trainingdb.GetTrainingDatasetByFilename(ctx, d, "a.jsonl"); got.Status != "stored" {
		t.Fatalf("stale sync changed it: %+v", got)
	}

	if _, err := trainingdb.SaveTrainingDataset(ctx, d, trainingdb.TrainingDataset{Filename: "b.jsonl", SizeBytes: 3, NumRecords: 1, SHA256: "h3"}); err != nil {
		t.Fatal(err)
	}
	if list, err := trainingdb.ListTrainingDatasets(ctx, d); err != nil || len(list) != 2 {
		t.Fatalf("ListTrainingDatasets: %+v, %v", list, err)
	}
	if _, err := trainingdb.MarkTrainingDatasetSynced(ctx, d, 999999, "x"); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := trainingdb.GetTrainingDatasetByFilename(ctx, d, "nope.jsonl"); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}
