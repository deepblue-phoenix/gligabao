package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_dataset"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
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

func TestQuery(t *testing.T) { dbtest.Run(t, testQuery) }

func testQuery(t *testing.T, d *sql.DB) {

	ex := `{"entities":[{"name":"Alice","type":"person","description":"engineer"},{"name":"Acme","type":"organization"},{"name":"Bob","type":"person"},{"name":"Rome","type":"place"}],
	"edges":[{"source":"Alice","target":"Acme","relation":"works_at"},{"source":"Bob","target":"Rome","relation":"lives_in"}]}`
	// ids: Alice=1 Acme=2 Bob=3 Rome=4; edges: works_at=1 lives_in=2
	llm := &recordingLLM{outs: []string{
		ex,
		"```json\n" + `{"answer":"Alice works at Acme.","entities":[1,999],"edges":[1,42]}` + "\n```",
		`{"answer":"Nothing relevant.","entities":[],"edges":[]}`,
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/query", Handler(d, llm, withVectors(t)))

	if _, err := memory_remember.Remember(context.Background(), d, llm, "u1", "", "", "x", time.Time{}); err != nil {
		t.Fatal(err)
	}

	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	rec := post("/v1/gb/query", `{"user_id":"u1","query":"Where does Alice work?"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res memory_query.QueryResult
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
	prompt := llm.reqs[1].Messages[0].Content
	if !strings.Contains(prompt, "Alice") || !strings.Contains(prompt, "works_at") || strings.Contains(prompt, "Bob") {
		t.Errorf("unexpected prompt:\n%s", prompt)
	}

	// No match: falls back to recent entities; empty evidence is serialized as [] not null.
	rec = post("/v1/gb/query", `{"user_id":"u1","query":"zzz"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"entities":[]`) || !strings.Contains(rec.Body.String(), `"edges":[]`) {
		t.Errorf("fallback: %d %s", rec.Code, rec.Body)
	}

	if rec := post("/v1/gb/query", `{"user_id":"u1","query":"  "}`); rec.Code != 400 {
		t.Errorf("empty query status = %d", rec.Code)
	}
}

func TestQueryEmptyGraph(t *testing.T) { dbtest.Run(t, testQueryEmptyGraph) }

func testQueryEmptyGraph(t *testing.T, d *sql.DB) {
	if _, err := memory_dataset.CreateDataset(context.Background(), d, "u1", "main", "", nil); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/query", Handler(d, &recordingLLM{}, withVectors(t))) // must not call the LLM
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/query", strings.NewReader(`{"user_id":"u1","query":"hi"}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"edges":[]`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestQueryNoLLM(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/query", Handler(nil, nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/query", strings.NewReader(`{"user_id":"u1","query":"x"}`)))
	if rec.Code != 503 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestQuerySession(t *testing.T) { dbtest.Run(t, testQuerySession) }

func testQuerySession(t *testing.T, d *sql.DB) {
	ex := `{"entities":[{"name":"Alice","type":"person"},{"name":"Acme","type":"organization"}],"edges":[{"source":"Alice","target":"Acme","relation":"works_at"}]}`
	llm := &recordingLLM{outs: []string{ex, `{"answer":"Acme.","entities":[1],"edges":[1]}`, `{"answer":"Acme.","entities":[1],"edges":[1]}`}}
	if _, err := memory_remember.Remember(context.Background(), d, llm, "u1", "", "", "x", time.Time{}); err != nil {
		t.Fatal(err)
	}
	prev := memory_core.TrajectoryDir
	memory_core.TrajectoryDir = t.TempDir()
	t.Cleanup(func() { memory_core.TrajectoryDir = prev })
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/query", Handler(d, llm, withVectors(t)))
	mux.HandleFunc("POST /v1/gb/eval/query", EvalHandler(d, llm, withVectors(t)))
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}

	// The session is echoed and does not change the answer or the evidence.
	var withSession, without memory_query.QueryResult
	rec := post("/v1/gb/query", `{"user_id":"u1","query":"Where does Alice work?","session_id":" s1 "}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &withSession); err != nil {
		t.Fatal(err)
	}
	rec = post("/v1/gb/query", `{"user_id":"u1","query":"Where does Alice work?"}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &without); err != nil {
		t.Fatal(err)
	}
	if withSession.SessionID != "s1" || without.SessionID != "" || strings.Contains(rec.Body.String(), "session_id") {
		t.Errorf("session IDs: %q, %q; body without: %s", withSession.SessionID, without.SessionID, rec.Body)
	}
	if withSession.Answer != without.Answer || len(withSession.Evidence.Edges) != len(without.Evidence.Edges) {
		t.Errorf("the session changed the result: %+v vs %+v", withSession, without)
	}

	// With eval, the trajectory carries the session ID.
	llm.outs = append(llm.outs, `{"answer":"Acme.","entities":[1],"edges":[1]}`)
	rec = post("/v1/gb/eval/query", `{"user_id":"u1","query":"Where does Alice work?","session_id":"s1"}`)
	var ev memory_query.QueryResult
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil || ev.Trajectory == nil || ev.Trajectory.SessionID != "s1" || ev.Trajectory.TrajectoryID == "s1" {
		t.Errorf("eval trajectory: %v %+v", err, ev.Trajectory)
	}

	if rec := post("/v1/gb/query", `{"user_id":"u1","query":"x","session_id":"a\tb"}`); rec.Code != 400 {
		t.Errorf("invalid session status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// The LLM call of a query is an inference, with no thinking (level none) unless the request names another.
func TestQueryInference(t *testing.T) { dbtest.Run(t, testQueryInference) }

func testQueryInference(t *testing.T, d *sql.DB) {
	llm := &recordingLLM{outs: []string{
		`{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`,
		`{"answer":"Alice.","entities":[1],"edges":[]}`,
		`{"answer":"Alice.","entities":[1],"edges":[]}`,
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/query", Handler(d, llm, withVectors(t)))
	if _, err := memory_remember.Remember(context.Background(), d, llm, "u1", "", "", "x", time.Time{}); err != nil {
		t.Fatal(err)
	}
	post := func(body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/query", strings.NewReader(body)))
		return rec.Code
	}
	if c := post(`{"user_id":"u1","query":"Alice?"}`); c != 200 {
		t.Fatalf("status %d", c)
	}
	if c := post(`{"user_id":"u1","query":"Alice?","reasoning":"High"}`); c != 200 {
		t.Fatalf("status %d", c)
	}
	if c := post(`{"user_id":"u1","query":"Alice?","reasoning":"max"}`); c != 400 {
		t.Fatalf("status %d, want 400", c)
	}
	if got := llm.reqs[len(llm.reqs)-2].Reasoning + "," + llm.reqs[len(llm.reqs)-1].Reasoning; got != "none,high" {
		t.Errorf("requested levels = %s", got)
	}
	rows, err := d.Query(`SELECT reasoning FROM gb_inferences WHERE type = 'query' AND stage = 'query' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var levels []string
	for rows.Next() {
		var l string
		rows.Scan(&l)
		levels = append(levels, l)
	}
	if strings.Join(levels, ",") != "none,high" {
		t.Errorf("recorded levels = %v", levels)
	}
	// What remember did ran without thinking.
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM gb_inferences WHERE type = 'remember' AND reasoning = 'none'`).Scan(&n); err != nil || n == 0 {
		t.Errorf("remember runs with no reasoning = %d, %v", n, err)
	}
}
