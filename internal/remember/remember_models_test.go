package remember

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestRememberModels(t *testing.T) { dbtest.Run(t, testRememberModels) }

// testRememberModels: the caller picks the model of each LLM stage of remember; a stage it names none for uses the
// default; and the extract_graph stage's model is the model_id of the entities and edges it yields.
func testRememberModels(t *testing.T, d *sql.DB) {
	const defaultModel = "default-model"
	place := "Globex"
	var requested []string // the model each request asked for ("" = none, so the client's default)
	llm := llmFunc(func(r ai.Request) (*ai.Response, error) {
		requested = append(requested, r.Model)
		model := r.Model
		if model == "" {
			model = defaultModel
		}
		out := `{"entities":[{"name":"Alice","type":"person"},{"name":"` + place + `","type":"other"}],
			"edges":[{"source":"Alice","target":"` + place + `","relation":"works_at","description":"Alice works at ` + place + `."}]}`
		if strings.Contains(r.System, "duplicate") || strings.Contains(r.Messages[0].Content, "<existing_facts>") {
			out = `{"duplicates":[],"contradictions":[]}`
		}
		return &ai.Response{Text: out, StopReason: "end_turn", Provider: ai.ProviderAnthropic, Model: model}, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm))
	post := func(body string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		return rec.Code
	}

	// Only extract is chosen: extract uses it, and nothing else asks for a model.
	if c := post(`{"user_id":"u1","text":"Alice works at Globex.","models":{"extract_graph":"big-model"}}`); c != 200 {
		t.Fatalf("status %d", c)
	}
	if len(requested) != 1 || requested[0] != "big-model" {
		t.Fatalf("requested = %q, want [big-model]", requested)
	}
	for _, e := range edges(t, d) {
		if e.ModelID != relational.ModelID("big-model") {
			t.Errorf("edge %d model_id = %q, want that of big-model", e.ID, e.ModelID)
		}
	}
	ms, err := relational.ListModels(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range ms {
		found = found || m.Name == "big-model"
	}
	if !found {
		t.Errorf("big-model is not registered: %+v", ms)
	}

	// A new fact about Alice: the stored one is a candidate, so the resolve_edges stage runs. Only resolve_edges is chosen (and
	// extract_image, empty, which means the default), so extract asks for no model.
	requested, place = nil, "Paris"
	if c := post(`{"user_id":"u1","text":"Alice moved to Paris.","models":{"resolve_edges":"small-model","extract_image":""}}`); c != 200 {
		t.Fatalf("status %d", c)
	}
	if len(requested) != 2 || requested[0] != "" || requested[1] != "small-model" {
		t.Fatalf("requested = %q, want [\"\" small-model]", requested)
	}
	for _, e := range edges(t, d) {
		want := "big-model"
		if e.Target == "Paris" {
			want = defaultModel // extracted with the default; the resolve model is not the facts' model
		}
		if e.ModelID != relational.ModelID(want) {
			t.Errorf("edge %d (%s) model_id = %q, want that of %s", e.ID, e.Target, e.ModelID, want)
		}
	}

	// No models at all: defaults everywhere.
	requested, place = nil, "Globex"
	if c := post(`{"user_id":"u1","text":"Alice works at Globex."}`); c != 200 || len(requested) != 1 || requested[0] != "" {
		t.Fatalf("no models: status %d, requested %q", c, requested)
	}

	// Bad input.
	for _, body := range []string{
		`{"user_id":"u1","text":"x","models":{"summarize":"m"}}`,
		`{"user_id":"u1","text":"x","models":{"extract_graph":"has space"}}`,
		`{"user_id":"u1","text":"x","models":{"extract_graph":"` + strings.Repeat("m", 200) + `"}}`,
		`{"user_id":"u1","text":"x","models":["extract_graph"]}`,
	} {
		if c := post(body); c != 400 {
			t.Errorf("%s: status %d, want 400", body, c)
		}
	}
}
