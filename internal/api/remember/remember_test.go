package remember

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivan/gligabao/internal/ai"
	"github.com/ivan/gligabao/internal/config"
	"github.com/ivan/gligabao/internal/db"
)

type fakeLLM struct{ out string }

func (f fakeLLM) Complete(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{Text: f.out, StopReason: "end_turn"}, nil
}

func TestRemember(t *testing.T) {
	d, err := db.Open(config.DBBackendSQLite, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

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
	count := func(table string) (n int) {
		d.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
		return
	}
	if n := count("gb_entities"); n != 3 {
		t.Errorf("entities = %d, want 3", n)
	}
	if n := count("gb_edges"); n != 2 {
		t.Errorf("edges = %d, want 2", n)
	}
	if n := count("gb_memories"); n != 2 {
		t.Errorf("memories = %d, want 2", n)
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
