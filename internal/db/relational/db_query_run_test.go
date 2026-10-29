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

func TestQueryRuns(t *testing.T) { dbtest.Run(t, queryRunsScenario) }

func queryRunsScenario(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	user := fmt.Sprintf("u-%d", time.Now().UnixNano())
	now := relational.Now()

	// A new run has every stage not started, and reads back as written.
	r := relational.NewQueryRun(user, "main", "Where does Alice live?", "sess-1", map[string]any{"models": map[string]any{"query": "m"}}, now)
	id, err := relational.CreateQueryRun(ctx, d, r)
	if err != nil || id == 0 {
		t.Fatalf("CreateQueryRun: %d, %v", id, err)
	}
	got, err := relational.GetQueryRun(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.UserID != user || got.Dataset != "main" || got.Question != "Where does Alice live?" || got.SessionID != "sess-1" ||
		got.Status != relational.RememberStarted || got.Error != nil || got.FinishedAt != nil || got.Params["models"] == nil || !got.StartedAt.Equal(now) {
		t.Fatalf("GetQueryRun: %+v", got)
	}
	if len(got.Stages) != len(relational.QueryStages) {
		t.Fatalf("stages: %+v", got.Stages)
	}
	for name, st := range got.Stages {
		if st.Status != relational.RememberNotStarted || st.Error != nil || st.StartedAt != nil || st.FinishedAt != nil || st.InferenceIDs == nil || len(st.InferenceIDs) != 0 {
			t.Fatalf("stage %s of a new run: %+v", name, st)
		}
	}

	// Saving overwrites the run and its stages, whatever their status.
	msg := "boom"
	got.Stages[relational.QueryStageRetrieveGraph] = relational.QueryStage{Status: relational.RememberSucceeded, StartedAt: &now, FinishedAt: &now, InferenceIDs: []int64{3}}
	got.Stages[relational.QueryStageDetectGeo] = relational.QueryStage{Status: relational.RememberSkipped, InferenceIDs: []int64{}}
	got.Stages[relational.QueryStageQuery] = relational.QueryStage{Status: relational.RememberFailed, Error: &msg, StartedAt: &now, FinishedAt: &now, InferenceIDs: []int64{4, 5}}
	got.Status, got.Error, got.FinishedAt = relational.RememberFailed, &msg, &now
	if err := relational.SaveQueryRun(ctx, d, *got); err != nil {
		t.Fatal(err)
	}
	back, err := relational.GetQueryRun(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	rg, dg, ng, q := back.Stages[relational.QueryStageRetrieveGraph], back.Stages[relational.QueryStageDetectGeo], back.Stages[relational.QueryStageNearbyGeo], back.Stages[relational.QueryStageQuery]
	if back.Status != relational.RememberFailed || back.Error == nil || *back.Error != "boom" || back.FinishedAt == nil {
		t.Fatalf("saved run: %+v", back)
	}
	if rg.Status != relational.RememberSucceeded || len(rg.InferenceIDs) != 1 || rg.InferenceIDs[0] != 3 || rg.StartedAt == nil || !rg.StartedAt.Equal(now) {
		t.Fatalf("retrieve_graph: %+v", rg)
	}
	if dg.Status != relational.RememberSkipped || ng.Status != relational.RememberNotStarted {
		t.Fatalf("detect_geo: %+v, nearby_geo: %+v", dg, ng)
	}
	if q.Status != relational.RememberFailed || q.Error == nil || *q.Error != "boom" || len(q.InferenceIDs) != 2 || q.InferenceIDs[1] != 5 {
		t.Fatalf("query: %+v", q)
	}
	if err := relational.SaveQueryRun(ctx, d, relational.QueryRun{ID: 99999}); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("saving a missing run: %v", err)
	}
	if _, err := relational.GetQueryRun(ctx, d, 99999); !errors.Is(err, relational.ErrNotFound) {
		t.Fatalf("missing run: %v", err)
	}

	// Listing, newest first, filtered on the run's status.
	ok := relational.NewQueryRun(user, "main", "q2", "", nil, now)
	ok.Status = relational.RememberSucceeded
	okID, err := relational.CreateQueryRun(ctx, d, ok)
	if err != nil {
		t.Fatal(err)
	}
	runs, total, err := relational.ListQueryRuns(ctx, d, relational.QueryRunFilter{UserID: user}, 10, 0)
	if err != nil || total != 2 || len(runs) != 2 || runs[0].ID != okID || runs[1].ID != id {
		t.Fatalf("ListQueryRuns: %+v, %d, %v", runs, total, err)
	}
	runs, total, err = relational.ListQueryRuns(ctx, d, relational.QueryRunFilter{UserID: user, Status: relational.RememberFailed}, 10, 0)
	if err != nil || total != 1 || len(runs) != 1 || runs[0].ID != id {
		t.Fatalf("ListQueryRuns failed: %+v, %d, %v", runs, total, err)
	}
	if _, total, _ = relational.ListQueryRuns(ctx, d, relational.QueryRunFilter{UserID: user, Dataset: "other"}, 10, 0); total != 0 {
		t.Fatalf("ListQueryRuns of another dataset: %d", total)
	}

	// The database refuses a status that is not one of the constants.
	bad := relational.NewQueryRun(user, "main", "q", "", nil, now)
	bad.Stages[relational.QueryStageQuery] = relational.QueryStage{Status: "done"}
	if _, err := relational.CreateQueryRun(ctx, d, bad); err == nil {
		t.Fatal("an unknown stage status was stored")
	}
}
