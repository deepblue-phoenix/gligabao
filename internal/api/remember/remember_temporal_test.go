package remember

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

func TestRememberBiTemporal(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

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
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, llm))

	post := func(body string) memory.Result {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var res memory.Result
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	r1 := post(`{"text":"Alice works at Acme.","reference_time":"2023-01-01T00:00:00Z"}`)
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

	r2 := post(`{"text":"Alice works at Globex since June 2024.","reference_time":"2024-07-01T00:00:00Z"}`)
	if len(r2.InvalidatedEdges) != 1 || r2.InvalidatedEdges[0] != 1 {
		t.Fatalf("invalidated = %v", r2.InvalidatedEdges)
	}
	var invalid, expired *string
	d.QueryRow(`SELECT invalid_at, expired_at FROM gb_edges WHERE id = 1`).Scan(&invalid, &expired)
	if invalid == nil || *invalid != "2024-06-01T00:00:00Z" || expired == nil {
		t.Errorf("edge 1 invalid_at=%v expired_at=%v", invalid, expired)
	}
	if p := llm.reqs[2].Messages[0].Content; !strings.Contains(p, "[1] Alice works at Acme.") {
		t.Errorf("resolve prompt:\n%s", p)
	}

	r3 := post(`{"text":"Alice works at Globex.","reference_time":"2024-08-01T00:00:00Z"}`)
	if len(r3.Edges) != 1 || r3.Edges[0].ID != 2 || r3.Edges[0].InvalidAt == nil || r3.Edges[0].InvalidAt.Year() != 2025 {
		t.Fatalf("edge 3: %+v", r3.Edges)
	}
	if len(llm.outs) != 0 {
		t.Errorf("unused LLM outputs: %d", len(llm.outs))
	}
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM gb_edges`).Scan(&n)
	if n != 2 {
		t.Errorf("edges = %d, want 2", n)
	}
}

func TestRememberSupersedesOlderFact(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
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
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, llm))
	for _, body := range []string{
		`{"text":"Alice works at Acme.","reference_time":"2023-01-01T00:00:00Z"}`,
		`{"text":"Alice started working at Globex.","reference_time":"2024-03-01T00:00:00Z"}`,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}
	type row struct{ valid, invalid, expired *string }
	get := func(id int) (r row) {
		d.QueryRow(`SELECT valid_at, invalid_at, expired_at FROM gb_edges WHERE id = ?`, id).Scan(&r.valid, &r.invalid, &r.expired)
		return
	}
	acme, globex := get(1), get(2)
	if *acme.valid != "2023-01-01T00:00:00Z" || acme.invalid == nil || *acme.invalid != "2024-03-01T00:00:00Z" || acme.expired == nil {
		t.Errorf("acme: %+v", acme)
	}
	if *globex.valid != "2024-03-01T00:00:00Z" || globex.invalid != nil || globex.expired != nil {
		t.Errorf("globex: %+v", globex)
	}
	var valid string
	d.QueryRow(`SELECT valid_at FROM gb_memories WHERE id = 2`).Scan(&valid)
	if valid != "2024-03-01T00:00:00Z" {
		t.Errorf("episode valid_at = %q", valid)
	}
}

func TestRememberOlderNewsIsSuperseded(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
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
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, llm))
	post := func(body string) memory.Result {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var res memory.Result
		json.Unmarshal(rec.Body.Bytes(), &res)
		return res
	}
	post(`{"text":"a","reference_time":"2024-01-01T00:00:00Z"}`)
	// News about 2019 arrives later: it is older than the stored fact, so the *new* fact ends when the stored one starts.
	r2 := post(`{"text":"b","reference_time":"2019-01-01T00:00:00Z"}`)
	if len(r2.InvalidatedEdges) != 1 || r2.InvalidatedEdges[0] != 2 {
		t.Fatalf("invalidated = %v", r2.InvalidatedEdges)
	}
	e := r2.Edges[0]
	if e.InvalidAt == nil || e.InvalidAt.Format("2006-01-02") != "2024-01-01" || e.ExpiredAt == nil {
		t.Errorf("new edge: %+v", e)
	}
	var invalid, expired *string
	d.QueryRow(`SELECT invalid_at, expired_at FROM gb_edges WHERE id = 1`).Scan(&invalid, &expired)
	if invalid != nil || expired != nil {
		t.Errorf("stored edge must be untouched: invalid_at=%v expired_at=%v", invalid, expired)
	}

	r3 := post(`{"text":"c"}`)
	if len(r3.Edges) != 1 || r3.Edges[0].ID != 1 {
		t.Errorf("repeat: %+v", r3.Edges)
	}
	if len(llm.outs) != 0 {
		t.Errorf("unused LLM outputs: %d", len(llm.outs))
	}
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM gb_edges`).Scan(&n)
	if n != 2 {
		t.Errorf("edges = %d, want 2", n)
	}
}

func TestRememberBadReferenceTime(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(nil, fakeLLM{}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(`{"text":"x","reference_time":"yesterday"}`)))
	if rec.Code != 400 {
		t.Errorf("status = %d", rec.Code)
	}
}
