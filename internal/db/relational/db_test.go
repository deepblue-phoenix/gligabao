package relational_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ivan/gligabao/internal/db/relational"
	"github.com/ivan/gligabao/internal/db/relational/dbtest"
)

// TestBackends runs the same scenario against every available backend (see dbtest).
func TestBackends(t *testing.T) { dbtest.Run(t, scenario) }

func scenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	user := fmt.Sprintf("u-%d", time.Now().UnixNano())
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Datasets.
	ds, err := relational.CreateDataset(ctx, d, user, "proj", "desc", []string{"a", "b"})
	check(err)
	if ds.ID == 0 || ds.Name != "proj" || len(ds.Tags) != 2 || ds.Deleted || ds.CreatedAt.IsZero() {
		t.Fatalf("dataset: %+v", ds)
	}
	if _, err := relational.CreateDataset(ctx, d, user, "proj", "", nil); !errors.Is(err, relational.ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := relational.DatasetID(ctx, d, user, "nope"); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}

	// EnsureDataset returns the live dataset, or creates it.
	dsID, err := relational.EnsureDataset(ctx, d, user, "proj")
	check(err)
	if dsID != ds.ID {
		t.Fatalf("EnsureDataset = %d, want %d", dsID, ds.ID)
	}
	other, err := relational.EnsureDataset(ctx, d, user, "main")
	check(err)
	if other == dsID {
		t.Fatal("EnsureDataset did not create a new dataset")
	}

	// LLM call log and dataset listing / soft delete.
	check(relational.RecordLLMCall(ctx, d, "remember", "anthropic", "claude", 10, 5, 0.01))
	if st, err := relational.LLMCallStats(ctx, d); err != nil || st["remember"] != (relational.LLMCallStat{Calls: 1, InputTokens: 10, OutputTokens: 5, CostUSD: 0.01}) {
		t.Fatalf("LLMCallStats: %+v, %v", st, err)
	}
	list, err := relational.ListDatasets(ctx, d, user, false)
	check(err)
	if len(list) != 2 {
		t.Fatalf("ListDatasets: %+v", list)
	}
	check(relational.DeleteDataset(ctx, d, user, "proj"))
	if err := relational.DeleteDataset(ctx, d, user, "proj"); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if list, err = relational.ListDatasets(ctx, d, user, false); err != nil || len(list) != 1 {
		t.Fatalf("ListDatasets after delete: %+v, %v", list, err)
	}
	if list, err = relational.ListDatasets(ctx, d, user, true); err != nil || len(list) != 2 || !list[0].Deleted || list[0].DeletedAt == nil {
		t.Fatalf("ListDatasets with deleted: %+v, %v", list, err)
	}
	if _, err := relational.CreateDataset(ctx, d, user, "proj", "", nil); err != nil { // the name can be reused
		t.Fatal(err)
	}
}
