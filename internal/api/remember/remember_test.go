package remember

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
)

type fakeLLM struct{ out string }

func (f fakeLLM) Complete(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{Text: f.out, StopReason: "end_turn"}, nil
}

func TestRemember(t *testing.T) { dbtest.Run(t, testRemember) }

func testRemember(t *testing.T, d *sql.DB) {

	llm := fakeLLM{"```json\n" + `{"entities":[{"name":"Alice","type":"Person","description":"engineer"},{"name":"Acme","type":"organization"}],
	"edges":[{"source":"Alice","target":"Acme","relation":"Works At"},{"source":"Alice","target":"Paris","relation":"lives in"}]}` + "\n```"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, llm))

	for i := 0; i < 2; i++ { // second call must be idempotent for entities/edges
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(`{"user_id":"u1","text":"Alice works at Acme and lives in Paris."}`)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}
	dsID, err := relational.DatasetID(context.Background(), d, "u1", relational.DefaultDataset)
	if err != nil {
		t.Fatal(err)
	}
	c, err := graph.DatasetCounts(context.Background(), d, dsID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Entities != 3 {
		t.Errorf("entities = %d, want 3", c.Entities)
	}
	if c.Edges != 2 {
		t.Errorf("edges = %d, want 2", c.Edges)
	}
	if c.Artifacts != 2 {
		t.Errorf("artifacts = %d, want 2", c.Artifacts)
	}

	// No kind was given, so both artifacts are "knowledge".
	arts, err := graph.ListArtifacts(context.Background(), d, dsID)
	if err != nil || len(arts) != 2 {
		t.Fatalf("artifacts: %v, %v", arts, err)
	}
	for _, a := range arts {
		if a.Kind != graph.ArtifactKnowledge {
			t.Errorf("artifact %d kind = %q, want %q", a.ID, a.Kind, graph.ArtifactKnowledge)
		}
	}

	// The second text repeated both facts, so they were merged into the stored edges (not inserted again): each
	// edge keeps the first artifact as its creator (artifact_id) and is linked to both artifacts.
	gedges, err := graph.ListGraphEdges(context.Background(), d, dsID)
	if err != nil || len(gedges) != 2 {
		t.Fatalf("edges: %v, %v", gedges, err)
	}
	for _, e := range gedges {
		if e.ArtifactID == nil || *e.ArtifactID != arts[0].ID {
			t.Errorf("edge %d artifact_id = %v, want creator %d", e.ID, e.ArtifactID, arts[0].ID)
		}
	}
	links, err := graph.ListEdgeArtifacts(context.Background(), d, dsID)
	if err != nil || len(links) != 4 {
		t.Fatalf("ListEdgeArtifacts: %+v, %v", links, err)
	}
	for i, e := range gedges {
		for j, a := range arts {
			if l := links[i*2+j]; l.EdgeID != e.ID || l.ArtifactID != a.ID {
				t.Errorf("link[%d] = %+v, want edge %d artifact %d", i*2+j, l, e.ID, a.ID)
			}
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(`{"user_id":"u1","text":"  "}`)))
	if rec.Code != 400 {
		t.Errorf("empty text status = %d", rec.Code)
	}
}

func TestRememberNoLLM(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(nil, nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(`{"user_id":"u1","text":"x"}`)))
	if rec.Code != 503 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestRememberKind(t *testing.T) { dbtest.Run(t, testRememberKind) }

func testRememberKind(t *testing.T, d *sql.DB) {
	llm := fakeLLM{`{"entities":[{"name":"Alice","type":"person"}],"edges":[]}`}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/gb/remember", Handler(d, llm))
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		return rec
	}

	for body, want := range map[string]string{
		`{"user_id":"u1","text":"a"}`:                      "knowledge", // default
		`{"user_id":"u1","text":"b","kind":"episode"}`:     "episode",
		`{"user_id":"u1","text":"c","kind":" Knowledge "}`: "knowledge", // trimmed, case-insensitive
	} {
		rec := post(body)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", body, rec.Code, rec.Body)
		}
		var res memory.Result
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res.Kind != want {
			t.Errorf("%s: kind = %q, want %q", body, res.Kind, want)
		}
	}
	dsID, err := relational.DatasetID(context.Background(), d, "u1", relational.DefaultDataset)
	if err != nil {
		t.Fatal(err)
	}
	arts, err := graph.ListArtifacts(context.Background(), d, dsID)
	if err != nil || len(arts) != 3 || arts[0].Kind != "knowledge" || arts[1].Kind != "episode" || arts[2].Kind != "knowledge" {
		t.Errorf("stored artifacts: %+v, %v", arts, err)
	}

	if rec := post(`{"user_id":"u1","text":"d","kind":"memo"}`); rec.Code != 400 {
		t.Errorf("invalid kind status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if arts, _ := graph.ListArtifacts(context.Background(), d, dsID); len(arts) != 3 {
		t.Errorf("an invalid kind stored an artifact: %d artifacts", len(arts))
	}
}
