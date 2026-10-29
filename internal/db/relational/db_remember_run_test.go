package relational_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestRememberRuns(t *testing.T) { dbtest.Run(t, rememberRunsScenario) }

func rememberRunsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	user := fmt.Sprintf("u-%d", time.Now().UnixNano())
	now := relational.Now()

	// A new run has every stage not started, and reads back as written.
	r := relational.NewRememberRun(user, "main", "knowledge", map[string]any{"web_search": true}, now)
	id, err := relational.CreateRememberRun(ctx, d, r)
	if err != nil || id == 0 {
		t.Fatalf("CreateRememberRun: %d, %v", id, err)
	}
	got, err := relational.GetRememberRun(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.UserID != user || got.Dataset != "main" || got.Kind != "knowledge" || got.Status != relational.RememberStarted ||
		got.Error != nil || got.FinishedAt != nil || got.ArtifactID != nil || got.Params["web_search"] != true || !got.StartedAt.Equal(now) {
		t.Fatalf("GetRememberRun: %+v", got)
	}
	if len(got.Stages) != len(relational.RememberStages) {
		t.Fatalf("stages: %+v", got.Stages)
	}
	for name, st := range got.Stages {
		if st.Status != relational.RememberNotStarted || st.Error != nil || st.StartedAt != nil || st.FinishedAt != nil || st.InferenceIDs == nil || len(st.InferenceIDs) != 0 {
			t.Fatalf("stage %s of a new run: %+v", name, st)
		}
	}

	// Saving overwrites the run and its stages.
	msg, artifact := "boom", int64(7)
	got.ArtifactID = &artifact
	got.Stages[relational.RememberStageExtractGraph] = relational.RememberStage{Status: relational.RememberSucceeded,
		StartedAt: &now, FinishedAt: &now, InferenceIDs: []int64{3, 4}}
	got.Stages[relational.RememberStageResolveEdges] = relational.RememberStage{Status: relational.RememberFailed, Error: &msg, StartedAt: &now,
		FinishedAt: &now, InferenceIDs: []int64{}}
	got.Stages[relational.RememberStageChunkText] = relational.RememberStage{Status: relational.RememberStarted, StartedAt: &now, InferenceIDs: []int64{}}
	got.Status, got.Error, got.FinishedAt = relational.RememberFailed, &msg, &now
	if err := relational.SaveRememberRun(ctx, d, *got); err != nil {
		t.Fatal(err)
	}
	back, err := relational.GetRememberRun(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	eg, re, ct, st := back.Stages[relational.RememberStageExtractGraph], back.Stages[relational.RememberStageResolveEdges],
		back.Stages[relational.RememberStageChunkText], back.Stages[relational.RememberStageStore]
	if back.Status != relational.RememberFailed || back.Error == nil || *back.Error != "boom" || back.FinishedAt == nil || back.ArtifactID == nil || *back.ArtifactID != 7 {
		t.Fatalf("saved run: %+v", back)
	}
	if eg.Status != relational.RememberSucceeded || eg.Error != nil || eg.StartedAt == nil || !eg.StartedAt.Equal(now) || len(eg.InferenceIDs) != 2 || eg.InferenceIDs[1] != 4 {
		t.Fatalf("extract_graph: %+v", eg)
	}
	if re.Status != relational.RememberFailed || re.Error == nil || *re.Error != "boom" {
		t.Fatalf("resolve_edges: %+v", re)
	}
	if ct.Status != relational.RememberStarted || ct.StartedAt == nil || ct.FinishedAt != nil {
		t.Fatalf("chunk_text: %+v", ct)
	}
	if st.Status != relational.RememberNotStarted {
		t.Fatalf("store: %+v", st)
	}
	if err := relational.SaveRememberRun(ctx, d, relational.RememberRun{ID: 99999}); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("saving a missing run: %v", err)
	}
	if _, err := relational.GetRememberRun(ctx, d, 99999); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("missing run: %v", err)
	}

	// A stage the call does not use is stored as skipped.
	got.Stages[relational.RememberStageWebSearch] = relational.RememberStage{Status: relational.RememberSkipped, InferenceIDs: []int64{}}
	if err := relational.SaveRememberRun(ctx, d, *got); err != nil {
		t.Fatal(err)
	}
	if back, err = relational.GetRememberRun(ctx, d, id); err != nil || back.Stages[relational.RememberStageWebSearch].Status != relational.RememberSkipped {
		t.Fatalf("a skipped stage: %+v, %v", back, err)
	}

	// Listing, newest first, filtered on the run's status.
	ok := relational.NewRememberRun(user, "main", "episode", nil, now)
	ok.Status = relational.RememberSucceeded
	okID, err := relational.CreateRememberRun(ctx, d, ok)
	if err != nil {
		t.Fatal(err)
	}
	runs, total, err := relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{UserID: user}, 10, 0)
	if err != nil || total != 2 || len(runs) != 2 || runs[0].ID != okID || runs[1].ID != id {
		t.Fatalf("ListRememberRuns: %+v, %d, %v", runs, total, err)
	}
	runs, total, err = relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{UserID: user, Status: relational.RememberFailed}, 10, 0)
	if err != nil || total != 1 || len(runs) != 1 || runs[0].ID != id {
		t.Fatalf("ListRememberRuns failed: %+v, %d, %v", runs, total, err)
	}
	if _, total, _ = relational.ListRememberRuns(ctx, d, relational.RememberRunFilter{UserID: user, Dataset: "other"}, 10, 0); total != 0 {
		t.Fatalf("ListRememberRuns of another dataset: %d", total)
	}

	// The database refuses a status that is not one of the constants.
	bad := relational.NewRememberRun(user, "main", "knowledge", nil, now)
	bad.Stages[relational.RememberStageStore] = relational.RememberStage{Status: "done"}
	if _, err := relational.CreateRememberRun(ctx, d, bad); err == nil {
		t.Fatal("an unknown stage status was stored")
	}
}

func TestRememberStageOutputs(t *testing.T) { dbtest.Run(t, rememberStageOutputsScenario) }

func rememberStageOutputsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	user := fmt.Sprintf("u-%d", time.Now().UnixNano())
	now := relational.Now()
	newRun := func() int64 {
		id, err := relational.CreateRememberRun(ctx, d, relational.NewRememberRun(user, "main", "knowledge", nil, now))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b := newRun(), newRun()
	add := func(run int64, stage, name, label, output string, at time.Time) {
		t.Helper()
		if id, err := relational.AddRememberStageOutput(ctx, d, relational.RememberStageOutput{RunID: run, Stage: stage, Name: name, Label: label,
			Output: json.RawMessage(output), CreatedAt: at}); err != nil || id == 0 {
			t.Fatalf("AddRememberStageOutput: %d, %v", id, err)
		}
	}
	old := now.Add(-48 * time.Hour)
	add(a, relational.RememberStageExtractGraph, "extract_graph", "chunk 1 of 2", `{"entities":[{"name":"Acme"}]}`, old)
	add(a, relational.RememberStageExtractGraph, "extract_graph", "chunk 2 of 2", `{"entities":[]}`, now)
	add(a, relational.RememberStageStore, "store", "", `{"artifact_id":7}`, now)
	add(b, relational.RememberStageResolveGeo, "detect_geo", "", `{"places":[]}`, now)

	// They come back in the order they were made, with their run, stage, name, label and JSON.
	got, err := relational.ListRememberStageOutputs(ctx, d, a, "")
	if err != nil || len(got) != 3 {
		t.Fatalf("ListRememberStageOutputs: %+v, %v", got, err)
	}
	if got[0].RunID != a || got[0].Stage != "extract_graph" || got[0].Name != "extract_graph" || got[0].Label != "chunk 1 of 2" ||
		got[1].Label != "chunk 2 of 2" || got[2].Stage != "store" || got[2].Label != "" || got[0].ID >= got[1].ID || got[1].ID >= got[2].ID {
		t.Fatalf("outputs: %+v", got)
	}
	var out struct{ Entities []struct{ Name string } }
	if err := json.Unmarshal(got[0].Output, &out); err != nil || len(out.Entities) != 1 || out.Entities[0].Name != "Acme" {
		t.Fatalf("output %s: %v", got[0].Output, err)
	}
	if !got[0].CreatedAt.Equal(old) {
		t.Fatalf("created_at %v, want %v", got[0].CreatedAt, old)
	}
	if only, err := relational.ListRememberStageOutputs(ctx, d, a, relational.RememberStageStore); err != nil || len(only) != 1 || only[0].Name != "store" {
		t.Fatalf("outputs of the store stage: %+v, %v", only, err)
	}
	if none, err := relational.ListRememberStageOutputs(ctx, d, 999999, ""); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("outputs of no run: %v, %v", none, err)
	}

	// The database refuses an output of an unknown stage, or of a run that does not exist.
	if _, err := relational.AddRememberStageOutput(ctx, d, relational.RememberStageOutput{RunID: a, Stage: "nope", Name: "x", Output: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("an output of an unknown stage was stored")
	}
	if _, err := relational.AddRememberStageOutput(ctx, d, relational.RememberStageOutput{RunID: 999999, Stage: "store", Name: "x", Output: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("an output of an unknown run was stored")
	}

	// Garbage collection: by age, leaving the runs alone.
	if _, err := relational.DeleteRememberStageOutputs(ctx, d, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ = relational.ListRememberStageOutputs(ctx, d, a, ""); len(got) != 2 || got[0].Label != "chunk 2 of 2" {
		t.Fatalf("after deleting the old ones: %+v", got)
	}
	if n, err := relational.DeleteRememberStageOutputs(ctx, d, time.Time{}); err != nil || n < 3 {
		t.Fatalf("deleting all: %d, %v", n, err)
	}
	if got, _ = relational.ListRememberStageOutputs(ctx, d, b, ""); len(got) != 0 {
		t.Fatalf("after deleting all: %+v", got)
	}
	if _, err := relational.GetRememberRun(ctx, d, a); err != nil {
		t.Fatalf("the run is gone: %v", err)
	}
}
