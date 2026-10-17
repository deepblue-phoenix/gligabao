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

// TestInferences checks the inferences endpoint of the admin UI: the totals and breakdowns, and the filtered, paged list.
func TestInferences(t *testing.T) { dbtest.Run(t, testInferences) }

func testInferences(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	h := admin.InferencesOf([]admin.Named{{Name: "standard", DB: d}})
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
		Totals     admin.InferenceSums        `json:"totals"`
		ByType     []admin.InferenceBreakdown `json:"by_type"`
		ByStage    []admin.InferenceBreakdown `json:"by_stage"`
		ByModel    []admin.InferenceBreakdown `json:"by_model"`
		Inferences []relational.Inference     `json:"inferences"`
		Total      int                        `json:"total"`
		Limit      int                        `json:"limit"`
		Offset     int                        `json:"offset"`
	}
	var before response
	if c := get("/x", &before); c != 200 {
		t.Fatalf("empty: %d", c)
	}

	for _, r := range []relational.Inference{
		{Type: "remember", Stage: "zz_stage", Model: "zz-model", Provider: "anthropic", Reasoning: "low", InputTokens: 10, OutputTokens: 5, CostUSD: 0.5},
		{Type: "remember", Stage: "zz_stage", Model: "zz-model", Provider: "anthropic", InputTokens: 20, OutputTokens: 1, CostUSD: 0.25},
		{Type: "recall", Stage: "zz_recall", InputTokens: 1, OutputTokens: 1}, // no model
	} {
		if _, err := relational.RecordInference(ctx, d, r); err != nil {
			t.Fatal(err)
		}
	}

	var all response
	if c := get("/x", &all); c != 200 {
		t.Fatalf("all: %d", c)
	}
	if all.Totals.Inferences != before.Totals.Inferences+3 || all.Totals.InputTokens != before.Totals.InputTokens+31 || all.Total != int(before.Totals.Inferences)+3 ||
		all.Limit != 100 || len(all.Inferences) != min(all.Total, 100) || all.Inferences[0].Stage != "zz_recall" {
		t.Errorf("all = %+v", all)
	}
	find := func(list []admin.InferenceBreakdown, key string) admin.InferenceBreakdown {
		for _, b := range list {
			if b.Key == key {
				return b
			}
		}
		t.Fatalf("no %q in %+v", key, list)
		return admin.InferenceBreakdown{}
	}
	if s := find(all.ByStage, "zz_stage"); s.Inferences != 2 || s.InputTokens != 30 || s.OutputTokens != 6 || s.CostUSD != 0.75 {
		t.Errorf("by stage = %+v", s)
	}
	if m := find(all.ByModel, relational.ModelID("zz-model")); m.Label != "zz-model" || m.Inferences != 2 {
		t.Errorf("by model = %+v", m)
	}

	// Filters apply to the list and its total, not to the totals; limit and offset page it.
	var f response
	if c := get("/x?stage=zz_stage&type=remember&reasoning=low", &f); c != 200 || f.Total != 1 || len(f.Inferences) != 1 || f.Inferences[0].Reasoning != "low" ||
		f.Inferences[0].Model != "zz-model" || f.Totals.Inferences != all.Totals.Inferences {
		t.Errorf("filtered: %d %+v", c, f)
	}
	if c := get("/x?model_id="+relational.ModelID("zz-model")+"&limit=1&offset=1", &f); c != 200 || f.Total != 2 || len(f.Inferences) != 1 || f.Inferences[0].Reasoning != "low" || f.Limit != 1 || f.Offset != 1 {
		t.Errorf("paged: %d %+v", c, f)
	}
	for url, want := range map[string]int{"/x?limit=0": 400, "/x?limit=a": 400, "/x?offset=-1": 400, "/x?db=nope": http.StatusNotFound} {
		if c := get(url); c != want {
			t.Errorf("%s: %d, want %d", url, c, want)
		}
	}
}
