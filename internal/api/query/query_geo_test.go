package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/geo"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// geoStubLLM answers by the system prompt: the extraction, the places of the question, the answer. It records the requests.
type geoStubLLM struct {
	mu      sync.Mutex
	places  string // the answer of detect_geo
	failGeo bool
	reqs    []ai.Request
}

func (f *geoStubLLM) Complete(_ context.Context, r ai.Request) (*ai.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	out := `{"answer":"Paris.","entities":[1],"edges":[]}`
	switch {
	case strings.Contains(r.System, "places that a question mentions"):
		if f.failGeo {
			return &ai.Response{Text: "not json", StopReason: "end_turn"}, nil
		}
		out = f.places
	case strings.Contains(r.System, "extract"):
		out = `{"entities":[{"name":"Alice","type":"person"},{"name":"Paris","type":"place"}],"edges":[]}`
	}
	return &ai.Response{Text: out, StopReason: "end_turn"}, nil
}

// requestOf returns the request whose system prompt contains s, nil if there is none.
func (f *geoStubLLM) requestOf(s string) *ai.Request {
	for i := range f.reqs {
		if strings.Contains(f.reqs[i].System, s) {
			return &f.reqs[i]
		}
	}
	return nil
}

type noResolver struct{}

func (noResolver) Name() string { return "none" }
func (noResolver) Resolve(context.Context, geo.Query) (*geo.Place, error) {
	return nil, geo.ErrNotFound
}

func TestQueryGeo(t *testing.T) { dbtest.Run(t, testQueryGeo) }

func testQueryGeo(t *testing.T, d *sql.DB) {
	llm := &geoStubLLM{places: `{"places":[
		{"text":"Paris","kind":"City","query":"Paris, France"},
		{"text":"paris","kind":"city","query":"dup"},
		{"text":"Atlantis","kind":"city","query":"made up: not in the question"},
		{"text":"the Alps","kind":"nonsense","query":""}]}`}
	if _, err := memory_remember.Remember(context.Background(), d, llm, "u1", "", "", "Alice lives in Paris.", time.Time{}); err != nil {
		t.Fatal(err)
	}
	post := func(h http.HandlerFunc, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/query", strings.NewReader(body)))
		return rec
	}
	body := `{"user_id":"u1","query":"Is Paris bigger than the Alps?"}`

	// Without a geo resolver the question is not looked at.
	llm.reqs = nil
	if rec := post(Handler(d, llm, withVectors(t)), body); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if llm.requestOf("places that a question mentions") != nil {
		t.Error("places were looked for without a geo resolver")
	}

	// With one: a separate LLM call, on the model asked for; the answer call has its own model.
	llm.reqs = nil
	h := Handler(d, llm, withVectors(t), memory_core.WithGeoResolver(noResolver{}))
	rec := post(h, `{"user_id":"u1","query":"Is Paris bigger than the Alps?","models":{"detect_geo":"geo-model","query":"answer-model"}}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "places") || strings.Contains(rec.Body.String(), "Paris, France") {
		t.Errorf("the places are in the response: %s", rec.Body)
	}
	geoReq := llm.requestOf("places that a question mentions")
	if geoReq == nil || geoReq.Model != "geo-model" || !strings.Contains(geoReq.Messages[0].Content, "Is Paris bigger than the Alps?") {
		t.Fatalf("detect_geo request: %+v", geoReq)
	}
	if strings.Contains(geoReq.Messages[0].Content, "Alice") { // only the question, not the graph
		t.Errorf("the graph was sent to detect_geo:\n%s", geoReq.Messages[0].Content)
	}
	if ans := llm.requestOf("ONLY a knowledge graph"); ans == nil || ans.Model != "answer-model" || strings.Contains(ans.Messages[0].Content, "Atlantis") {
		t.Errorf("answer request: %+v", ans)
	}
	var stages []string
	rows, err := d.Query(`SELECT stage FROM gb_inferences WHERE type = 'query' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		rows.Scan(&s)
		stages = append(stages, s)
	}
	if strings.Join(stages, ",") != "embed,query,embed,detect_geo,query" { // the 1st query had no geo; every query embeds the question first
		t.Errorf("recorded stages = %v", stages)
	}

	// The places themselves (the Go result and the trajectory).
	res, err := memory_query.Query(context.Background(), d, llm, "u1", "", "Is Paris bigger than the Alps?",
		withVectors(t), memory_core.WithGeoResolver(noResolver{}), memory_core.WithEval(), memory_core.WithTrajectoryDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	want := []memory_query.QueryPlace{{Text: "Paris", Kind: "city", Query: "Paris, France"}, {Text: "the Alps", Kind: "place", Query: "the Alps"}}
	if len(res.Geo) != 2 || res.Geo[0] != want[0] || res.Geo[1] != want[1] {
		t.Errorf("places = %+v", res.Geo)
	}
	var found bool
	for _, s := range res.Trajectory.Steps {
		for _, tc := range s.ToolCalls {
			if tc.FunctionName == "detect_geo" {
				found = true
			}
		}
	}
	if !found {
		t.Error("no detect_geo step in the trajectory")
	}
	res.Trajectory = nil // it holds the LLM's raw answer
	if b, _ := json.Marshal(res); strings.Contains(string(b), "Paris, France") {
		t.Errorf("the places are in the JSON: %s", b)
	}

	// A failure of the detection does not fail the query.
	llm.failGeo = true
	res, err = memory_query.Query(context.Background(), d, llm, "u1", "", "Is Paris bigger than the Alps?", withVectors(t), memory_core.WithGeoResolver(noResolver{}))
	if err != nil || res.Answer != "Paris." || len(res.Geo) != 0 {
		t.Errorf("failing detection: %+v, %v", res, err)
	}

	// Reasoning per stage; a string is the level of the answering call; a stage left out has none.
	for _, c := range []struct{ reasoning, geo, answer string }{
		{`{"detect_geo":"low","query":"high"}`, "low", "high"},
		{`{"query":"Medium"}`, "none", "medium"},
		{`"high"`, "none", "high"},
		{`null`, "none", "none"},
	} {
		llm.reqs, llm.failGeo = nil, false
		if rec := post(h, `{"user_id":"u1","query":"Is Paris bigger than the Alps?","reasoning":`+c.reasoning+`}`); rec.Code != 200 {
			t.Fatalf("reasoning %s: status %d: %s", c.reasoning, rec.Code, rec.Body)
		}
		g, a := llm.requestOf("places that a question mentions"), llm.requestOf("ONLY a knowledge graph")
		if g == nil || a == nil || g.Reasoning != c.geo || a.Reasoning != c.answer {
			t.Errorf("reasoning %s: detect_geo %+v, answer %+v; want %s, %s", c.reasoning, g, a, c.geo, c.answer)
		}
	}
	for _, r := range []string{`{"extract_graph":"low"}`, `{"query":"max"}`, `{"query":3}`, `3`, `["low"]`} {
		if rec := post(h, `{"user_id":"u1","query":"q","reasoning":`+r+`}`); rec.Code != 400 {
			t.Errorf("reasoning %s: status %d", r, rec.Code)
		}
	}

	// Models: another stage is a 400, and the stages of remember are not the ones of query.
	for _, m := range []string{`{"extract_graph":"x"}`, `{"nope":"x"}`} {
		if rec := post(h, `{"user_id":"u1","query":"q","models":`+m+`}`); rec.Code != 400 {
			t.Errorf("models %s: status %d", m, rec.Code)
		}
	}
}
