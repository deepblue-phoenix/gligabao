package admin_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// TestRememberRuns checks the remember runs endpoint of the admin UI: the counts by status, and the filtered, paged list.
func TestRememberRuns(t *testing.T) { dbtest.Run(t, testRememberRuns) }

func testRememberRuns(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	h := admin.RememberRunsOf([]admin.Named{{Name: "standard", DB: d}})
	type response struct {
		Counts admin.RememberRunCounts  `json:"counts"`
		Stages []string                 `json:"stages"`
		Runs   []relational.RememberRun `json:"runs"`
		Total  int                      `json:"total"`
		Limit  int                      `json:"limit"`
		Offset int                      `json:"offset"`
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
		run := relational.NewRememberRun("u", s.dataset, "knowledge", map[string]any{"k": "v"}, time.Now().UTC())
		id, err := relational.CreateRememberRun(ctx, d, run)
		if err != nil {
			t.Fatal(err)
		}
		run.ID, run.Status = id, s.status
		if err := relational.SaveRememberRun(ctx, d, run); err != nil {
			t.Fatal(err)
		}
	}

	var all response
	if c := get("/x", &all); c != 200 {
		t.Fatalf("all: %d", c)
	}
	b, a := before.Counts, all.Counts
	if a.Total != b.Total+3 || a.Started != b.Started+1 || a.Succeeded != b.Succeeded+1 || a.Failed != b.Failed+1 ||
		all.Total != a.Total || all.Limit != 100 || all.Runs[0].Dataset != "zz_b" || len(all.Stages) != len(relational.RememberStages) ||
		len(all.Runs[0].Stages) != len(all.Stages) {
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

// TestRememberRunOutputs checks the stage outputs endpoint: the outputs of one run, in order; a run with none; unknown runs.
func TestRememberRunOutputs(t *testing.T) { dbtest.Run(t, testRememberRunOutputs) }

func testRememberRunOutputs(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	h := admin.RememberRunOutputsOf([]admin.Named{{Name: "standard", DB: d}})
	get := func(url string, out any) int {
		t.Helper()
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", url, nil))
		if out != nil && rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code
	}
	withOutputs, err := relational.CreateRememberRun(ctx, d, relational.NewRememberRun("u", "zz_o", "knowledge", nil, time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	without, err := relational.CreateRememberRun(ctx, d, relational.NewRememberRun("u", "zz_o", "knowledge", nil, time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"extract_graph", "resolve_edges"} {
		stage := relational.RememberStageExtractGraph
		if i == 1 {
			stage = relational.RememberStageResolveEdges
		}
		if _, err := relational.AddRememberStageOutput(ctx, d, relational.RememberStageOutput{RunID: withOutputs, Stage: stage, Name: name,
			Label: "chunk 1 of 1", Output: json.RawMessage(`{"n":` + string(rune('1'+i)) + `}`)}); err != nil {
			t.Fatal(err)
		}
	}
	var got struct {
		RunID   int64                            `json:"run_id"`
		Stage   string                           `json:"stage"`
		Outputs []relational.RememberStageOutput `json:"outputs"`
	}
	if c := get("/x?run_id="+strconv.FormatInt(withOutputs, 10), &got); c != 200 || got.RunID != withOutputs || len(got.Outputs) != 2 ||
		got.Outputs[0].Name != "extract_graph" || got.Outputs[1].Stage != "resolve_edges" || got.Outputs[1].Label != "chunk 1 of 1" || string(got.Outputs[1].Output) != `{"n":2}` {
		t.Fatalf("outputs: %d %+v", c, got)
	}
	got.Outputs = nil
	if c := get("/x?run_id="+strconv.FormatInt(withOutputs, 10)+"&stage=resolve_edges", &got); c != 200 || got.Stage != "resolve_edges" || len(got.Outputs) != 1 || got.Outputs[0].Name != "resolve_edges" {
		t.Fatalf("outputs of one stage: %d %+v", c, got)
	}
	got.Outputs = nil
	if c := get("/x?run_id="+strconv.FormatInt(without, 10), &got); c != 200 || got.Outputs == nil || len(got.Outputs) != 0 {
		t.Fatalf("run without outputs: %d %+v", c, got)
	}
	for url, want := range map[string]int{"/x": 400, "/x?run_id=abc": 400, "/x?run_id=0": 400, "/x?run_id=1&stage=nope": 400, "/x?run_id=99999999": 404, "/x?run_id=1&db=nope": 404} {
		if c := get(url, nil); c != want {
			t.Errorf("%s: %d, want %d", url, c, want)
		}
	}
}

// scriptedLLM answers the extraction with the next canned text and the comparison of facts with the contradiction of fact 1.
type scriptedLLM struct{ extractions []string }

func (s *scriptedLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	out := `{"duplicates":[],"contradictions":[{"new":0,"existing":1}]}`
	if !strings.HasPrefix(req.System, "You compare NEW facts") {
		out, s.extractions = s.extractions[0], s.extractions[1:]
	}
	return &ai.Response{Text: out, StopReason: "end_turn", Model: "fake-model"}, nil
}

// TestRememberRunTemporal checks the temporal endpoint: what a run did to the facts (created, invalidated), with the artifact
// reference time; a run that stored nothing; unknown runs.
func TestRememberRunTemporal(t *testing.T) { dbtest.Run(t, testRememberRunTemporal) }

func testRememberRunTemporal(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	fact := func(org string) string {
		return `{"entities":[{"name":"Alice","type":"person"},{"name":"` + org + `","type":"organization"}],
		"edges":[{"source":"Alice","target":"` + org + `","relation":"works_at","description":"Alice works at ` + org + `."}]}`
	}
	llm := &scriptedLLM{extractions: []string{fact("Acme"), fact("Globex")}}
	ref1, ref2 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	r1, err := memory_remember.Remember(ctx, d, llm, "u1", "main", "knowledge", "Alice works at Acme.", ref1)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := memory_remember.Remember(ctx, d, llm, "u1", "main", "knowledge", "Alice started working at Globex.", ref2)
	if err != nil {
		t.Fatal(err)
	}

	type response struct {
		RunID      int64 `json:"run_id"`
		ArtifactID int64 `json:"artifact_id"`
		Artifacts  []struct {
			ID      int64     `json:"id"`
			Kind    string    `json:"kind"`
			ValidAt time.Time `json:"valid_at"`
		} `json:"artifacts"`
		Events []graph.EdgeEvent `json:"events"`
	}
	h := admin.RememberRunTemporalOf([]admin.Named{{Name: "standard", DB: d}})
	get := func(url string, out *response) int {
		t.Helper()
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", url, nil))
		if out != nil && rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code
	}

	var first, second response
	if c := get("/x?run_id="+strconv.FormatInt(r1.RememberRunID, 10), &first); c != 200 || first.ArtifactID != r1.ArtifactID ||
		len(first.Events) != 1 || first.Events[0].Event != graph.EdgeCreated || first.Events[0].Edge == nil || first.Events[0].Edge.Target != "Acme" ||
		len(first.Artifacts) != 1 || !first.Artifacts[0].ValidAt.Equal(ref1) {
		t.Errorf("first run: %d %+v", c, first)
	}
	// The second run created the Globex fact and closed the Acme one at its start.
	if c := get("/x?run_id="+strconv.FormatInt(r2.RememberRunID, 10), &second); c != 200 || len(second.Events) != 2 ||
		second.Events[0].Event != graph.EdgeCreated || second.Events[0].Edge.Target != "Globex" {
		t.Fatalf("second run: %d %+v", c, second)
	}
	inv := second.Events[1]
	if inv.Event != graph.EdgeInvalidated || inv.Edge.Target != "Acme" || inv.InvalidAtBefore != nil || inv.InvalidAtAfter == nil || !inv.InvalidAtAfter.Equal(ref2) ||
		inv.CausedByEdgeID != second.Events[0].EdgeID || inv.Edge.ExpiredAt == nil || !inv.CreatedAt.Equal(*inv.Edge.ExpiredAt) {
		t.Errorf("invalidation: %+v", inv)
	}

	// A run that stored nothing has no events.
	id, err := relational.CreateRememberRun(ctx, d, relational.NewRememberRun("u1", "main", "knowledge", nil, time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	var none response
	if c := get("/x?run_id="+strconv.FormatInt(id, 10), &none); c != 200 || none.ArtifactID != 0 || len(none.Events) != 0 || none.Events == nil {
		t.Errorf("no artifact: %d %+v", c, none)
	}
	for url, want := range map[string]int{"/x": 400, "/x?run_id=a": 400, "/x?run_id=999999": 404, "/x?run_id=1&db=nope": 404} {
		if c := get(url, nil); c != want {
			t.Errorf("%s: %d, want %d", url, c, want)
		}
	}
}
