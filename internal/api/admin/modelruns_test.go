package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

// TestModelRuns checks the model runs endpoint of the admin UI: the totals and breakdowns, and the filtered, paged list.
func TestModelRuns(t *testing.T) { dbtest.Run(t, testModelRuns) }

func testModelRuns(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	h := admin.ModelRunsOf([]admin.Named{{Name: "standard", DB: d}})
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
	type response struct {
		Totals  admin.ModelRunSums        `json:"totals"`
		ByType  []admin.ModelRunBreakdown `json:"by_type"`
		ByStage []admin.ModelRunBreakdown `json:"by_stage"`
		ByModel []admin.ModelRunBreakdown `json:"by_model"`
		Runs    []relational.ModelRun     `json:"runs"`
		Total   int                       `json:"total"`
		Limit   int                       `json:"limit"`
		Offset  int                       `json:"offset"`
	}
	var before response
	if c := get("/x", &before); c != 200 {
		t.Fatalf("empty: %d", c)
	}

	for _, r := range []relational.ModelRun{
		{Type: "remember", Stage: "zz_stage", Model: "zz-model", Provider: "anthropic", Reasoning: "low", InputTokens: 10, OutputTokens: 5, CostUSD: 0.5},
		{Type: "remember", Stage: "zz_stage", Model: "zz-model", Provider: "anthropic", InputTokens: 20, OutputTokens: 1, CostUSD: 0.25},
		{Type: "recall", Stage: "zz_recall", InputTokens: 1, OutputTokens: 1}, // no model
	} {
		if _, err := relational.RecordModelRun(ctx, d, r); err != nil {
			t.Fatal(err)
		}
	}

	var all response
	if c := get("/x", &all); c != 200 {
		t.Fatalf("all: %d", c)
	}
	if all.Totals.Runs != before.Totals.Runs+3 || all.Totals.InputTokens != before.Totals.InputTokens+31 || all.Total != int(before.Totals.Runs)+3 ||
		all.Limit != 100 || len(all.Runs) != min(all.Total, 100) || all.Runs[0].Stage != "zz_recall" {
		t.Errorf("all = %+v", all)
	}
	find := func(list []admin.ModelRunBreakdown, key string) admin.ModelRunBreakdown {
		for _, b := range list {
			if b.Key == key {
				return b
			}
		}
		t.Fatalf("no %q in %+v", key, list)
		return admin.ModelRunBreakdown{}
	}
	if s := find(all.ByStage, "zz_stage"); s.Runs != 2 || s.InputTokens != 30 || s.OutputTokens != 6 || s.CostUSD != 0.75 {
		t.Errorf("by stage = %+v", s)
	}
	if m := find(all.ByModel, relational.ModelID("zz-model")); m.Label != "zz-model" || m.Runs != 2 {
		t.Errorf("by model = %+v", m)
	}

	// Filters apply to the list and its total, not to the totals; limit and offset page it.
	var f response
	if c := get("/x?stage=zz_stage&type=remember&reasoning=low", &f); c != 200 || f.Total != 1 || len(f.Runs) != 1 || f.Runs[0].Reasoning != "low" ||
		f.Runs[0].Model != "zz-model" || f.Totals.Runs != all.Totals.Runs {
		t.Errorf("filtered: %d %+v", c, f)
	}
	if c := get("/x?model_id="+relational.ModelID("zz-model")+"&limit=1&offset=1", &f); c != 200 || f.Total != 2 || len(f.Runs) != 1 || f.Runs[0].Reasoning != "low" || f.Limit != 1 || f.Offset != 1 {
		t.Errorf("paged: %d %+v", c, f)
	}
	for url, want := range map[string]int{"/x?limit=0": 400, "/x?limit=a": 400, "/x?offset=-1": 400, "/x?db=nope": http.StatusNotFound} {
		if c := get(url); c != want {
			t.Errorf("%s: %d, want %d", url, c, want)
		}
	}
}
