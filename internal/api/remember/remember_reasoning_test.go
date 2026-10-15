package remember

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// summaryLLM answers a summary request with a summary, and records the model and reasoning level of those requests.
type summaryLLM struct{ models, levels []string }

func (l *summaryLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	if strings.Contains(req.System, "summarize a passage") {
		l.models, l.levels = append(l.models, req.Model), append(l.levels, req.Reasoning)
		return &ai.Response{Text: "A summary.", StopReason: "end_turn", Model: "sum-model"}, nil
	}
	return &ai.Response{Text: `{"entities":[],"edges":[]}`, StopReason: "end_turn", Model: "fake-model"}, nil
}

func TestRememberSummaryModelAndReasoning(t *testing.T) {
	dbtest.Run(t, testRememberSummaryModelAndReasoning)
}

func testRememberSummaryModelAndReasoning(t *testing.T, d *sql.DB) {
	llm := &summaryLLM{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, llm, memory_core.WithChunking(chunk.Config{Size: 40, OverlapPercent: 20})))
	post := func(body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		return rec.Code
	}
	text := strings.Repeat("Alice walked along the river and talked about the weather and her plans for the summer holidays.\n\n", 6)

	if c := post(`{"user_id":"u1","dataset":"d","text":` + jsonString(text) + `,"models":{"summarize_chunk":"small-model"},"reasoning":{"summarize_chunk":"Low"}}`); c != 200 {
		t.Fatalf("status %d", c)
	}
	if len(llm.levels) < 2 {
		t.Fatalf("%d summary requests", len(llm.levels))
	}
	for i := range llm.levels {
		if llm.models[i] != "small-model" || llm.levels[i] != "low" {
			t.Errorf("summary request %d: model %q, reasoning %q", i, llm.models[i], llm.levels[i])
		}
	}
	dsID, _ := relational.DatasetID(context.Background(), d, "u1", "d")
	arts, _ := graph.ListArtifacts(context.Background(), d, dsID)
	n := 0
	for _, a := range arts {
		if a.Kind == graph.ArtifactChunkSummary {
			n++
			if a.Reasoning != "low" || a.ModelID != relational.ModelID("sum-model") || a.Text != "A summary." {
				t.Errorf("summary artifact %+v", a)
			}
		}
	}
	if n != len(llm.levels) {
		t.Errorf("%d summary artifacts, %d requests", n, len(llm.levels))
	}

	// Without reasoning the level is recorded as "none" and no level is requested.
	llm.models, llm.levels = nil, nil
	if c := post(`{"user_id":"u1","dataset":"e","text":` + jsonString(text) + `}`); c != 200 {
		t.Fatalf("status %d", c)
	}
	dsID, _ = relational.DatasetID(context.Background(), d, "u1", "e")
	arts, _ = graph.ListArtifacts(context.Background(), d, dsID)
	for _, a := range arts {
		if a.Kind == graph.ArtifactChunkSummary && a.Reasoning != "none" {
			t.Errorf("summary artifact %+v", a)
		}
	}
	for _, l := range llm.levels {
		if l != "" {
			t.Errorf("reasoning %q requested", l)
		}
	}

	for _, body := range []string{
		`{"user_id":"u1","text":"x","reasoning":{"extract_graph":"low"}}`,
		`{"user_id":"u1","text":"x","reasoning":{"summarize_chunk":"max"}}`,
		`{"user_id":"u1","image":"aGk=","reasoning":{"summarize_chunk":"low"}}`,
	} {
		if c := post(body); c != 400 {
			t.Errorf("%s: status %d, want 400", body, c)
		}
	}
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer("\n", `\n`, `"`, `\"`).Replace(s) + `"`
}
