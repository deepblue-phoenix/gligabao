package remember

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
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
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

// edges returns the stored edges of user u1's default dataset by ID, and edge returns the one with the given ID.
func edges(t *testing.T, d *sql.DB) []graph.Edge {
	t.Helper()
	list, err := graph.ListEdges(context.Background(), d, "u1", relational.DefaultDataset)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func edge(t *testing.T, d *sql.DB, id int64) graph.Edge {
	t.Helper()
	for _, e := range edges(t, d) {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no edge %d", id)
	return graph.Edge{}
}

// ts formats a stored time as RFC 3339 UTC, or "<nil>" if unset.
func ts(t *time.Time) string {
	if t == nil {
		return "<nil>"
	}
	return t.UTC().Format(time.RFC3339)
}

func TestRememberBiTemporal(t *testing.T) { dbtest.Run(t, testRememberBiTemporal) }

func testRememberBiTemporal(t *testing.T, d *sql.DB) {

	ents := func(org, relation, validAt, invalidAt string) string {
		return `{"entities":[{"name":"Alice","type":"person"},{"name":"` + org + `","type":"organization"}],
		"edges":[{"source":"Alice","target":"` + org + `","relation":"` + relation + `","description":"Alice works at ` + org + `.","valid_at":` + validAt + `,"invalid_at":` + invalidAt + `}]}`
	}
	llm := &recordingLLM{outs: []string{
		// 1) No date in the text: valid_at falls back to the reference time.
		ents("Acme", "Works At", "null", "null"),
		// 2) Starts at an explicit date; the LLM also returns bogus ids/indexes, which are ignored.
		ents("Globex", "works_at", `"2024-06-01"`, "null"),
		`{"duplicates":[],"contradictions":[{"new":0,"existing":1},{"new":0,"existing":999},{"new":7,"existing":1}]}`,
		// 3) Exact repeat of fact 2: deduplicated without an LLM resolve call; the end date is filled in.
		ents("GLOBEX", "works_at", "null", `"2025-01-01T00:00:00Z"`),
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm))

	post := func(body string) memory_remember.Result {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var res memory_remember.Result
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	r1 := post(`{"user_id":"u1","text":"Alice works at Acme.","reference_time":"2023-01-01T00:00:00Z"}`)
	if r1.ValidAt.Year() != 2023 || r1.CreatedAt.IsZero() {
		t.Errorf("episode times: %+v", r1)
	}
	if len(r1.Edges) != 1 || r1.Edges[0].ID != 1 || r1.Edges[0].Relation != "works_at" ||
		r1.Edges[0].ValidAt == nil || r1.Edges[0].ValidAt.Format("2006-01-02") != "2023-01-01" ||
		r1.Edges[0].InvalidAt != nil || r1.Edges[0].ExpiredAt != nil || r1.Edges[0].CreatedAt == nil {
		t.Fatalf("edge 1: %+v", r1.Edges)
	}
	if got := llm.reqs[0].Messages[0].Content; !strings.Contains(got, "2023-01-01T00:00:00Z") {
		t.Errorf("extract prompt lacks reference time:\n%s", got)
	}

	r2 := post(`{"user_id":"u1","text":"Alice works at Globex since June 2024.","reference_time":"2024-07-01T00:00:00Z"}`)
	if len(r2.InvalidatedEdges) != 1 || r2.InvalidatedEdges[0] != 1 {
		t.Fatalf("invalidated = %v", r2.InvalidatedEdges)
	}
	if e := edge(t, d, 1); ts(e.InvalidAt) != "2024-06-01T00:00:00Z" || e.ExpiredAt == nil {
		t.Errorf("edge 1 invalid_at=%v expired_at=%v", ts(e.InvalidAt), ts(e.ExpiredAt))
	}
	if p := llm.reqs[2].Messages[0].Content; !strings.Contains(p, "[1] Alice works at Acme.") {
		t.Errorf("resolve prompt:\n%s", p)
	}

	r3 := post(`{"user_id":"u1","text":"Alice works at Globex.","reference_time":"2024-08-01T00:00:00Z"}`)
	if len(r3.Edges) != 1 || r3.Edges[0].ID != 2 || r3.Edges[0].InvalidAt == nil || r3.Edges[0].InvalidAt.Year() != 2025 {
		t.Fatalf("edge 3: %+v", r3.Edges)
	}
	if len(llm.outs) != 0 {
		t.Errorf("unused LLM outputs: %d", len(llm.outs))
	}
	if n := len(edges(t, d)); n != 2 {
		t.Errorf("edges = %d, want 2", n)
	}
}

func TestRememberSupersedesOlderFact(t *testing.T) { dbtest.Run(t, testRememberSupersedesOlderFact) }

func testRememberSupersedesOlderFact(t *testing.T, d *sql.DB) {
	fact := func(org, validAt string) string {
		return `{"entities":[{"name":"Alice","type":"person"},{"name":"` + org + `","type":"organization"}],
		"edges":[{"source":"Alice","target":"` + org + `","relation":"works_at","description":"Alice works at ` + org + `.","valid_at":` + validAt + `}]}`
	}
	llm := &recordingLLM{outs: []string{
		fact("Acme", "null"),
		fact("Globex", "null"),
		`{"duplicates":[],"contradictions":[{"new":0,"existing":1}]}`,
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm))
	for _, body := range []string{
		`{"user_id":"u1","text":"Alice works at Acme.","reference_time":"2023-01-01T00:00:00Z"}`,
		`{"user_id":"u1","text":"Alice started working at Globex.","reference_time":"2024-03-01T00:00:00Z"}`,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}
	acme, globex := edge(t, d, 1), edge(t, d, 2)
	if ts(acme.ValidAt) != "2023-01-01T00:00:00Z" || ts(acme.InvalidAt) != "2024-03-01T00:00:00Z" || acme.ExpiredAt == nil {
		t.Errorf("acme: %+v", acme)
	}
	if ts(globex.ValidAt) != "2024-03-01T00:00:00Z" || globex.InvalidAt != nil || globex.ExpiredAt != nil {
		t.Errorf("globex: %+v", globex)
	}
	dsID, err := relational.DatasetID(context.Background(), d, "u1", relational.DefaultDataset)
	if err != nil {
		t.Fatal(err)
	}
	mems, err := graph.ListArtifacts(context.Background(), d, dsID)
	if err != nil || len(mems) != 2 {
		t.Fatalf("artifacts: %v, %v", mems, err)
	}
	if got := mems[1].ValidAt.Format(time.RFC3339); got != "2024-03-01T00:00:00Z" {
		t.Errorf("episode valid_at = %q", got)
	}

	// Provenance: each edge is anchored to the artifact it came from, and the entity shared by both artifacts
	// (Alice) is linked to both, while each organization is linked only to its own.
	gedges, err := graph.ListGraphEdges(context.Background(), d, dsID)
	if err != nil || len(gedges) != 2 {
		t.Fatalf("edges: %v, %v", gedges, err)
	}
	for i, e := range gedges {
		if e.ArtifactID == nil || *e.ArtifactID != mems[i].ID {
			t.Errorf("edge %d artifact_id = %v, want %d", e.ID, e.ArtifactID, mems[i].ID)
		}
	}
	ents, err := graph.ListEntities(context.Background(), d, dsID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]int64{}
	for _, e := range ents {
		byName[e.Name] = e.ID
	}
	links, err := graph.ListEntityArtifacts(context.Background(), d, dsID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64][]int64{}
	for _, l := range links {
		got[l.EntityID] = append(got[l.EntityID], l.ArtifactID)
	}
	same := func(a, b []int64) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	for name, want := range map[string][]int64{
		"Alice":  {mems[0].ID, mems[1].ID},
		"Acme":   {mems[0].ID},
		"Globex": {mems[1].ID},
	} {
		if !same(got[byName[name]], want) {
			t.Errorf("%s linked to artifacts %v, want %v", name, got[byName[name]], want)
		}
	}
}

func TestRememberOlderNewsIsSuperseded(t *testing.T) {
	dbtest.Run(t, testRememberOlderNewsIsSuperseded)
}

func testRememberOlderNewsIsSuperseded(t *testing.T, d *sql.DB) {
	fact := func(org string) string {
		return `{"entities":[{"name":"Alice","type":"person"},{"name":"` + org + `","type":"organization"}],
		"edges":[{"source":"Alice","target":"` + org + `","relation":"works_at","description":"Alice works at ` + org + `."}]}`
	}
	llm := &recordingLLM{outs: []string{
		fact("Acme"),
		fact("Initech"),
		`{"duplicates":[],"contradictions":[{"new":0,"existing":1}]}`,
		fact("acme"), // exact repeat of the (still live) Acme fact: no resolve call
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, d, llm))
	post := func(body string) memory_remember.Result {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var res memory_remember.Result
		json.Unmarshal(rec.Body.Bytes(), &res)
		return res
	}
	post(`{"user_id":"u1","text":"a","reference_time":"2024-01-01T00:00:00Z"}`)
	// News about 2019 arrives later: it is older than the stored fact, so the *new* fact ends when the stored one starts.
	r2 := post(`{"user_id":"u1","text":"b","reference_time":"2019-01-01T00:00:00Z"}`)
	if len(r2.InvalidatedEdges) != 1 || r2.InvalidatedEdges[0] != 2 {
		t.Fatalf("invalidated = %v", r2.InvalidatedEdges)
	}
	e := r2.Edges[0]
	if e.InvalidAt == nil || e.InvalidAt.Format("2006-01-02") != "2024-01-01" || e.ExpiredAt == nil {
		t.Errorf("new edge: %+v", e)
	}
	if e := edge(t, d, 1); e.InvalidAt != nil || e.ExpiredAt != nil {
		t.Errorf("stored edge must be untouched: invalid_at=%v expired_at=%v", ts(e.InvalidAt), ts(e.ExpiredAt))
	}

	r3 := post(`{"user_id":"u1","text":"c"}`)
	if len(r3.Edges) != 1 || r3.Edges[0].ID != 1 {
		t.Errorf("repeat: %+v", r3.Edges)
	}
	if len(llm.outs) != 0 {
		t.Errorf("unused LLM outputs: %d", len(llm.outs))
	}
	if n := len(edges(t, d)); n != 2 {
		t.Errorf("edges = %d, want 2", n)
	}
}

func TestRememberBadReferenceTime(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", testHandler(t, nil, fakeLLM{}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(`{"user_id":"u1","text":"x","reference_time":"yesterday"}`)))
	if rec.Code != 400 {
		t.Errorf("status = %d", rec.Code)
	}
}
