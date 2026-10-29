package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

// TestQueryRuns checks the query runs endpoint of the admin API: the counts by status, and the filtered, paged list.
func TestQueryRuns(t *testing.T) { dbtest.Run(t, testQueryRuns) }

func testQueryRuns(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	h := admin.QueryRunsOf([]admin.Named{{Name: "standard", DB: d}})
	type response struct {
		Counts admin.QueryRunCounts  `json:"counts"`
		Stages []string              `json:"stages"`
		Runs   []relational.QueryRun `json:"runs"`
		Total  int                   `json:"total"`
		Limit  int                   `json:"limit"`
		Offset int                   `json:"offset"`
	}
	get := func(url string, out ...any) int {
		t.Helper()
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", url, nil))
		if len(out) > 0 && rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), out[0]); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code
	}
	var before response
	if c := get("/x", &before); c != 200 {
		t.Fatalf("empty: %d", c)
	}

	for _, s := range []struct{ dataset, status string }{{"zz_a", relational.RememberSucceeded}, {"zz_a", relational.RememberFailed}, {"zz_b", relational.RememberStarted}} {
		run := relational.NewQueryRun("u", s.dataset, "q?", "", map[string]any{"k": "v"}, time.Now().UTC())
		id, err := relational.CreateQueryRun(ctx, d, run)
		if err != nil {
			t.Fatal(err)
		}
		run.ID, run.Status = id, s.status
		run.Stages[relational.QueryStageDetectGeo] = relational.QueryStage{Status: relational.RememberSkipped, InferenceIDs: []int64{}}
		if err := relational.SaveQueryRun(ctx, d, run); err != nil {
			t.Fatal(err)
		}
	}

	var all response
	if c := get("/x", &all); c != 200 {
		t.Fatalf("all: %d", c)
	}
	b, a := before.Counts, all.Counts
	if a.Total != b.Total+3 || a.Started != b.Started+1 || a.Succeeded != b.Succeeded+1 || a.Failed != b.Failed+1 ||
		all.Total != a.Total || all.Limit != 100 || all.Runs[0].Dataset != "zz_b" || len(all.Stages) != len(relational.QueryStages) ||
		len(all.Runs[0].Stages) != len(all.Stages) || all.Runs[0].Stages["detect_geo"].Status != relational.RememberSkipped {
		t.Errorf("all = %+v", all)
	}

	// Filters apply to the list and its total, not to the counts; limit and offset page it.
	var f response
	if c := get("/x?dataset=zz_a&status=failed", &f); c != 200 || f.Total != 1 || len(f.Runs) != 1 || f.Runs[0].Status != relational.RememberFailed || f.Counts != a {
		t.Errorf("filtered: %d %+v", c, f)
	}
	if c := get("/x?dataset=zz_a&limit=1&offset=1", &f); c != 200 || f.Total != 2 || len(f.Runs) != 1 || f.Limit != 1 || f.Offset != 1 {
		t.Errorf("paged: %d %+v", c, f)
	}
	for url, want := range map[string]int{"/x?limit=0": 400, "/x?limit=a": 400, "/x?offset=-1": 400, "/x?status=zz": 400, "/x?db=nope": http.StatusNotFound} {
		if c := get(url); c != want {
			t.Errorf("%s: %d, want %d", url, c, want)
		}
	}
}
