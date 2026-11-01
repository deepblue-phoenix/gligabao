package remember

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

func TestRememberGraphIndex(t *testing.T) { dbtest.Run(t, testRememberGraphIndex) }

// graph_index reaches the graph embedder hook (trimmed); a malformed name, a name without a hook and a name with an image are a 400.
func testRememberGraphIndex(t *testing.T, d *sql.DB) {
	llm := fakeLLM{`{"entities":[{"name":"Alice","type":"person"},{"name":"Acme","type":"organization"}],
	"edges":[{"source":"Alice","target":"Acme","relation":"works_at"}]}`}
	var mu sync.Mutex
	var got []string
	hook := memory_core.WithGraphEmbedder(func(_ context.Context, req memory_core.GraphEmbedRequest) ([]float32, error) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, req.Index)
		return []float32{1}, nil
	})
	post := func(h http.HandlerFunc, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/gb/remember", strings.NewReader(body)))
		return rec
	}
	with := testHandler(t, d, llm, hook)
	for _, body := range []string{
		`{"user_id":"u1","text":"Alice works at Acme.","graph_index":" gcnii-small "}`,
		`{"user_id":"u1","text":"Alice works at Acme."}`,
	} {
		if rec := post(with, body); rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", body, rec.Code, rec.Body)
		}
	}
	if len(got) != 2 || got[0] != "gcnii-small" || got[1] != "" {
		t.Errorf("the hook got the indexes %q, want [gcnii-small \"\"]", got)
	}
	for _, name := range []string{"../x", "a/b", "-x", strings.Repeat("x", 65)} {
		if rec := post(with, `{"user_id":"u1","text":"Alice works at Acme.","graph_index":"`+name+`"}`); rec.Code != 400 {
			t.Errorf("graph_index %q: status %d, want 400", name, rec.Code)
		}
	}
	if rec := post(with, `{"user_id":"u1","image":"AAAA","graph_index":"i"}`); rec.Code != 400 {
		t.Errorf("graph_index with an image: status %d, want 400", rec.Code)
	}
	if rec := post(testHandler(t, d, llm), `{"user_id":"u1","text":"x","graph_index":"i"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "hook") {
		t.Errorf("graph_index without a hook: status %d: %s", rec.Code, rec.Body)
	}
}
