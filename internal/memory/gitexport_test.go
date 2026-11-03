package memory_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_gitexport"
)

// TestLoadGitExport checks what LoadGitExport reads: the graph classes, the run logs of the dataset with their inferences and stage
// outputs, and not the runs of other users' datasets of the same name or of an earlier dataset of the user that was deleted.
func TestLoadGitExport(t *testing.T) { dbtest.Run(t, testLoadGitExport) }

func testLoadGitExport(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)

	// An earlier dataset called "w" of the user, with a run, and another user's dataset of that name.
	_, err := relational.CreateDataset(ctx, d, "u1", "w", "", nil)
	must(err)
	_, err = relational.CreateRememberRun(ctx, d, relational.NewRememberRun("u1", "w", "knowledge", nil, now.Add(-2*time.Hour)))
	must(err)
	_, err = relational.CreateDataset(ctx, d, "u2", "w", "", nil)
	must(err)
	_, err = relational.CreateRememberRun(ctx, d, relational.NewRememberRun("u2", "w", "knowledge", nil, now.Add(time.Hour)))
	must(err)
	must(relational.DeleteDataset(ctx, d, "u1", "w"))

	// The dataset the export is of: runs after the deletion belong to it.
	ds, err := relational.CreateDataset(ctx, d, "u1", "w", "second", []string{"t"})
	must(err)
	inf, err := relational.RecordInference(ctx, d, relational.Inference{Type: "remember", Stage: "extract_graph", Model: "claude-x", Provider: "anthropic", Reasoning: "none", InputTokens: 10, OutputTokens: 2})
	must(err)
	tx, err := graph.Begin(ctx, d)
	must(err)
	defer tx.Rollback()
	art, err := tx.InsertArtifact(ctx, ds.ID, graph.ArtifactKnowledge, "A text.", "", now, now)
	must(err)
	must(tx.Commit())
	_, err = graph.InsertArtifactClasses(ctx, d, ds.ID, art, 0, []string{"news"}, now)
	must(err)

	run := relational.NewRememberRun("u1", "w", "knowledge", map[string]any{"chunk_size": 1024}, now.Add(time.Minute))
	run.ArtifactID = &art
	run.Status = relational.RememberSucceeded
	st := run.Stages[relational.RememberStageExtractGraph]
	st.Status, st.InferenceIDs = relational.RememberSucceeded, []int64{inf}
	run.Stages[relational.RememberStageExtractGraph] = st
	runID, err := relational.CreateRememberRun(ctx, d, run)
	must(err)
	_, err = relational.AddRememberStageOutput(ctx, d, relational.RememberStageOutput{RunID: runID, Stage: relational.RememberStageExtractGraph, Name: "extract_graph", Output: []byte(`{"a":1}`)})
	must(err)
	_, err = relational.CreateQueryRun(ctx, d, relational.NewQueryRun("u1", "w", "What?", "", nil, now.Add(2*time.Minute)))
	must(err)

	in, err := memory.LoadGitExport(ctx, d, "u1", "w")
	must(err)
	if in.Name != "w" || in.Description != "second" || len(in.Artifacts) != 1 {
		t.Fatalf("input = %+v", in)
	}
	if len(in.Classes) != 1 || in.Classes[0].Class != "news" || in.Classes[0].ArtifactID != art {
		t.Errorf("classes = %+v", in.Classes)
	}
	if len(in.RememberRuns) != 1 || in.RememberRuns[0].Run.ID != runID {
		t.Fatalf("remember runs = %+v (the runs of the earlier dataset and of another user must be left out)", in.RememberRuns)
	}
	l := in.RememberRuns[0]
	if got := l.Inferences[inf]; got.Model != "claude-x" || got.InputTokens != 10 {
		t.Errorf("inference = %+v", got)
	}
	if len(l.Outputs) != 1 || l.Outputs[0].Name != "extract_graph" {
		t.Errorf("outputs = %+v", l.Outputs)
	}
	if len(in.QueryRuns) != 1 || in.QueryRuns[0].Run.Question != "What?" {
		t.Errorf("query runs = %+v", in.QueryRuns)
	}

	// It builds a repository: the call's commit, and a commit for the query.
	res, err := memory_gitexport.Write(*in, memory_gitexport.DirSink{Dir: t.TempDir()})
	if err != nil || res.Commits != 3 || res.Runs != 2 {
		t.Errorf("write: %+v, %v", res, err)
	}

	if _, err := memory.LoadGitExport(ctx, d, "u1", "nope"); err != memory_core.ErrDatasetNotFound {
		t.Errorf("unknown dataset: %v", err)
	}
}
