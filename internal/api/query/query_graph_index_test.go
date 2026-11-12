package query

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"

	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil/httputiltest"
)

func TestQueryGraphIndex(t *testing.T) { dbtest.Run(t, testQueryGraphIndex) }

// graph_index reaches the graph embedder hook (trimmed); a malformed name and a name without a hook are a 400.
func testQueryGraphIndex(t *testing.T,
	d *sql.DB) {
	ex := `{"entities":[{"name":"Alice","type":"person"},{"name":"Acme","type":"organization"}],
	"edges":[{"source":"Alice","target":"Acme","relation":"works_at"}]}`
	answer := `{"answer":"Alice works at Acme.","entities":[1],"edges":[1]}`
	llm := &recordingLLM{
		outs: []string{ex, answer, answer, answer},
	}
	if _, err := memory_remember.Remember(context.Background(),
		d,
		llm,
		"u1",
		"",
		"",
		"x",
		time.Time{}); err != nil {
		t.Fatal(err)
	}
	var got []string
	hook := memory_core.WithGraphEmbedder(func(_ context.Context,
		req memory_core.GraphEmbedRequest) ([]float32, error) {
		got = append(got, req.Index)
		return []float32{1}, nil
	})
	post := func(h http.HandlerFunc,
		body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httputiltest.NewRequest("POST",
			"/v1/gb/query",
			strings.NewReader(body)))
		return rec
	}
	with := Handler(d,
		llm,
		withVectors(t),
		hook)
	for _, body := range []string{
		`{"user_id":"u1","query":"Where does Alice work?","graph_index":" gcnii-small "}`,
		`{"user_id":"u1","query":"Where does Alice work?"}`,
	} {
		if rec := post(with, body); rec.Code != 200 {
			t.Fatalf("%s: status %d: %s",
				body,
				rec.Code,
				rec.Body)
		}
	}
	if len(got) != 2 || got[0] != "gcnii-small" || got[1] != "" {
		t.Errorf("the hook got the indexes %q, want [gcnii-small \"\"]", got)
	}
	if rec := post(with, `{"user_id":"u1","query":"q","graph_index":"a/b"}`); rec.Code != 400 {
		t.Errorf("malformed graph_index: status %d, want 400", rec.Code)
	}
	if rec := post(Handler(d,
		llm,
		withVectors(t)), `{"user_id":"u1","query":"q","graph_index":"i"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "hook") {
		t.Errorf("graph_index without a hook: status %d: %s",
			rec.Code,
			rec.Body)
	}
}
