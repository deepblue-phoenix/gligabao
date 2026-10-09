package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivan/gligabao/internal/ai"
	"github.com/ivan/gligabao/internal/db"
	"github.com/ivan/gligabao/internal/memory"
)

// recordingLLM returns canned outputs in order and records requests.
type recordingLLM struct {
	outs []string
	reqs []ai.Request
}

func (f *recordingLLM) Complete(_ context.Context, r ai.Request) (*ai.Response, error) {
	f.reqs = append(f.reqs, r)
	out := f.outs[0]
	f.outs = f.outs[1:]
	return &ai.Response{Text: out, StopReason: "end_turn"}, nil
}

func TestRecall(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ex := `{"entities":[{"name":"Alice","type":"person","description":"engineer"},{"name":"Acme","type":"organization"},{"name":"Bob","type":"person"},{"name":"Rome","type":"place"}],
	"edges":[{"source":"Alice","target":"Acme","relation":"works_at"},{"source":"Bob","target":"Rome","relation":"lives_in"}]}`
	// ids: Alice=1 Acme=2 Bob=3 Rome=4; edges: works_at=1 lives_in=2
	llm := &recordingLLM{outs: []string{
		ex,
		`{"events":[]}`,
		"```json\n" + `{"answer":"Alice works at Acme.","entities":[1,999],"edges":[1,42]}` + "\n```",
		`{"answer":"Nothing relevant.","entities":[],"edges":[]}`,
	}}
	mux := http.NewServeMux()
	Register(mux, d, llm)

	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	if rec := post("/v1/gb/remember", `{"text":"x"}`); rec.Code != 200 {
		t.Fatalf("remember: %d %s", rec.Code, rec.Body)
	}

	rec := post("/v1/gb/recall", `{"query":"Where does Alice work?"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res memory.RecallResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Answer != "Alice works at Acme." {
		t.Errorf("answer = %q", res.Answer)
	}
	// Bogus IDs dropped; cited edge's endpoints included.
	if len(res.Evidence.Edges) != 1 || res.Evidence.Edges[0].Relation != "works_at" {
		t.Errorf("edges = %+v", res.Evidence.Edges)
	}
	if len(res.Evidence.Entities) != 2 || res.Evidence.Entities[0].Name != "Alice" || res.Evidence.Entities[1].Name != "Acme" {
		t.Errorf("entities = %+v", res.Evidence.Entities)
	}
	// Context sent to the LLM holds Alice's neighbourhood but not the unrelated Bob/Rome.
	prompt := llm.reqs[2].Messages[0].Content
	if !strings.Contains(prompt, "Alice") || !strings.Contains(prompt, "works_at") || strings.Contains(prompt, "Bob") {
		t.Errorf("unexpected prompt:\n%s", prompt)
	}

	// No match: falls back to recent entities; empty evidence is serialized as [] not null.
	rec = post("/v1/gb/recall", `{"query":"zzz"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"entities":[]`) || !strings.Contains(rec.Body.String(), `"edges":[]`) {
		t.Errorf("fallback: %d %s", rec.Code, rec.Body)
	}

	if rec := post("/v1/gb/recall", `{"query":"  "}`); rec.Code != 400 {
		t.Errorf("empty query status = %d", rec.Code)
	}
}

func TestRecallEmptyGraph(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	mux := http.NewServeMux()
	Register(mux, d, &recordingLLM{}) // must not call the LLM
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/recall", strings.NewReader(`{"query":"hi"}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"edges":[]`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestRecallNoLLM(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, nil, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/recall", strings.NewReader(`{"query":"x"}`)))
	if rec.Code != 503 {
		t.Errorf("status = %d", rec.Code)
	}
}
