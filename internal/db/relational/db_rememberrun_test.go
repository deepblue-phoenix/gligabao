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
		if st.Status != relational.RememberNotStarted || st.Error != nil || st.StartedAt != nil || st.FinishedAt != nil || st.ModelRunIDs == nil || len(st.ModelRunIDs) != 0 {
			t.Fatalf("stage %s of a new run: %+v", name, st)
		}
	}

	// Saving overwrites the run and its stages.
	msg, artifact := "boom", int64(7)
	got.ArtifactID = &artifact
	got.Stages[relational.RememberStageExtractGraph] = relational.RememberStage{Status: relational.RememberSucceeded,
		StartedAt: &now, FinishedAt: &now, ModelRunIDs: []int64{3, 4}}
	got.Stages[relational.RememberStageResolveEdges] = relational.RememberStage{Status: relational.RememberFailed, Error: &msg, StartedAt: &now,
		FinishedAt: &now, ModelRunIDs: []int64{}}
	got.Stages[relational.RememberStageChunkText] = relational.RememberStage{Status: relational.RememberStarted, StartedAt: &now, ModelRunIDs: []int64{}}
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
	if eg.Status != relational.RememberSucceeded || eg.Error != nil || eg.StartedAt == nil || !eg.StartedAt.Equal(now) || len(eg.ModelRunIDs) != 2 || eg.ModelRunIDs[1] != 4 {
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
