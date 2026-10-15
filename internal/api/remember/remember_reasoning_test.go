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
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// summaryLLM answers a summary request with a summary, and records the model and reasoning level of those requests.
type summaryLLM struct{ models, levels, all []string }

func (l *summaryLLM) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	l.all = append(l.all, req.Reasoning)
	if strings.Contains(req.System, "summarize a passage") {
		l.models, l.levels = append(l.models, req.Model), append(l.levels, req.Reasoning)
		return &ai.Response{Text: "A summary.", StopReason: "end_turn", Model: "sum-model", InputTokens: 7, OutputTokens: 3, CostUSD: 0.5}, nil
	}
	return &ai.Response{Text: `{"entities":[],"edges":[]}`, StopReason: "end_turn", Model: "fake-model"}, nil
}

func TestRememberSummaryModelAndReasoning(t *testing.T) {
	dbtest.Run(t, testRememberSummaryModelAndReasoning)
}

func testRememberSummaryModelAndReasoning(t *testing.T, d *sql.DB) {
	llm := &summaryLLM{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm, memory_core.WithChunking(chunk.Config{Size: 40, OverlapPercent: 20})))
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
			if a.ModelRunID == 0 || a.Text != "A summary." {
				t.Errorf("summary artifact %+v", a)
			}
		}
	}
	if n != len(llm.levels) {
		t.Errorf("%d summary artifacts, %d requests", n, len(llm.levels))
	}

	// The run that wrote each summary tracks the model and the level, and the artifact references it.
	for _, a := range arts {
		if a.Kind != graph.ArtifactChunkSummary {
			continue
		}
		run, err := relational.GetModelRun(context.Background(), d, a.ModelRunID)
		if err != nil || run.Stage != memory_core.StageSummarizeChunk || run.Type != memory_remember.CallRemember || run.Reasoning != "low" ||
			run.Model != "sum-model" || run.ModelID != relational.ModelID("sum-model") || run.InputTokens != 7 || run.OutputTokens != 3 || run.CostUSD != 0.5 {
			t.Errorf("summary %d: model run %+v, %v", a.ID, run, err)
		}
	}

	// Without reasoning every stage runs with the default level, none (the summary too), and it is recorded.
	llm.models, llm.levels, llm.all = nil, nil, nil
	if c := post(`{"user_id":"u1","dataset":"e","text":` + jsonString(text) + `}`); c != 200 {
		t.Fatalf("status %d", c)
	}
	dsID, _ = relational.DatasetID(context.Background(), d, "u1", "e")
	arts, _ = graph.ListArtifacts(context.Background(), d, dsID)
	for _, a := range arts {
		if a.Kind != graph.ArtifactChunkSummary {
			continue
		}
		if run, err := relational.GetModelRun(context.Background(), d, a.ModelRunID); err != nil || run.Reasoning != "none" {
			t.Errorf("summary %d: model run %+v, %v", a.ID, run, err)
		}
	}
	if len(llm.levels) == 0 || len(llm.all) == 0 {
		t.Fatal("no LLM request")
	}
	for _, l := range llm.all {
		if l != "none" {
			t.Errorf("an LLM stage ran with reasoning %q, want none", l)
		}
	}

	// Any stage can think: here the extraction does, and the summary does not.
	llm.levels, llm.all = nil, nil
	if c := post(`{"user_id":"u1","dataset":"f","text":` + jsonString(text) + `,"reasoning":{"extract_graph":"medium"}}`); c != 200 {
		t.Fatalf("status %d", c)
	}
	for _, l := range llm.levels {
		if l != "none" {
			t.Errorf("summary reasoning %q requested, want none", l)
		}
	}
	got := map[string]bool{}
	for _, l := range llm.all {
		got[l] = true
	}
	if !got["medium"] {
		t.Errorf("the extraction did not run with medium: %v", llm.all)
	}

	for _, body := range []string{
		`{"user_id":"u1","text":"x","reasoning":{"web_search":"low"}}`,
		`{"user_id":"u1","text":"x","reasoning":{"recall":"low"}}`,
		`{"user_id":"u1","text":"x","reasoning":{"summarize_chunk":"max"}}`,
		`{"user_id":"u1","image":"aGk=","reasoning":{"extract_image":"max"}}`,
	} {
		if c := post(body); c != 400 {
			t.Errorf("%s: status %d, want 400", body, c)
		}
	}

	// Every stage of the run table: the model run of each LLM call has its stage and level.
	rows, err := d.Query(`SELECT stage, reasoning FROM gb_model_runs WHERE type = 'remember'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	stages := map[string]bool{}
	for rows.Next() {
		var stage, level string
		rows.Scan(&stage, &level)
		stages[stage] = true
		if level == "" {
			t.Errorf("run without a level")
		}
	}
	if !stages[memory_core.StageExtractGraph] || !stages[memory_core.StageSummarizeChunk] {
		t.Errorf("stages of the runs: %v", stages)
	}
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer("\n", `\n`, `"`, `\"`).Replace(s) + `"`
}
