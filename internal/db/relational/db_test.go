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
	runID, err := relational.RecordInference(ctx, d, relational.Inference{Type: "remember", Stage: "extract_graph", Model: "claude", Provider: "anthropic",
		Reasoning: "Low", InputTokens: 10, OutputTokens: 5, CostUSD: 0.01})
	check(err)
	if run, err := relational.GetInference(ctx, d, runID); err != nil || run.Type != "remember" || run.Stage != "extract_graph" || run.ModelID != relational.ModelID("claude") ||
		run.Model != "claude" || run.Provider != "anthropic" || run.Kind != "llm" || run.Reasoning != "low" || run.InputTokens != 10 || run.OutputTokens != 5 ||
		run.CostUSD != 0.01 || run.CreatedAt.IsZero() {
		t.Fatalf("GetInference: %+v, %v", run, err)
	}
	// No model reported and no level: a run without a model, with reasoning none.
	noModel, err := relational.RecordInference(ctx, d, relational.Inference{Type: "query", InputTokens: 1})
	check(err)
	if run, err := relational.GetInference(ctx, d, noModel); err != nil || run.ModelID != "" || run.Reasoning != "none" {
		t.Fatalf("GetInference without model: %+v, %v", run, err)
	}
	if _, err := relational.RecordInference(ctx, d, relational.Inference{Type: "remember", Model: "claude", Reasoning: "max"}); err == nil {
		t.Fatal("an unknown reasoning level was recorded")
	}
	if _, err := relational.GetInference(ctx, d, 12345); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("missing run: %v", err)
	}
	if st, err := relational.InferenceStats(ctx, d); err != nil || st["remember"] != (relational.InferenceStat{Inferences: 1, InputTokens: 10, OutputTokens: 5, CostUSD: 0.01}) || st["query"].Inferences != 1 {
		t.Fatalf("InferenceStats: %+v, %v", st, err)
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
