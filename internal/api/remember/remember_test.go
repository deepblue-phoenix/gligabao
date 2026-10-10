package remember

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ivan/gligabao/internal/ai"
	"github.com/ivan/gligabao/internal/db/graph"
	"github.com/ivan/gligabao/internal/db/relational"
	"github.com/ivan/gligabao/internal/db/relational/dbtest"
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
	if c.Memories != 2 {
		t.Errorf("memories = %d, want 2", c.Memories)
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
