package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestGraphExport(t *testing.T) { dbtest.Run(t, testGraphExport) }

func testGraphExport(t *testing.T, d *sql.DB) {
	mux := http.NewServeMux()
	Register(mux, &db.DB{Relational: d, Graph: d}, echoLLM{}, nil, nil, nil, nil)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	want := func(rec *httptest.ResponseRecorder, code int) {
		t.Helper()
		if rec.Code != code {
			t.Fatalf("status %d, want %d: %s", rec.Code, code, rec.Body)
		}
	}

	want(do("GET", "/v1/gb/datasets/main/graph", ""), 400)            // user_id required
	want(do("GET", "/v1/gb/datasets/main/graph?user_id=u1", ""), 404) // no such dataset
	want(do("GET", "/v1/gb/datasets/a%20b/graph?user_id=u1", ""), 404)

	// An empty dataset exports empty arrays, not null.
	want(do("POST", "/v1/gb/datasets", `{"user_id":"u1","name":"empty"}`), 201)
	rec := do("GET", "/v1/gb/datasets/empty/graph?user_id=u1", "")
	want(rec, 200)
	if !strings.Contains(rec.Body.String(), `"entities":[]`) || !strings.Contains(rec.Body.String(), `"edges":[]`) {
		t.Errorf("empty graph: %s", rec.Body)
	}

	want(do("POST", "/v1/gb/remember", `{"user_id":"u1","text":"Alice works at Acme."}`), 200)
	want(do("POST", "/v1/gb/remember", `{"user_id":"u2","text":"Alice works at Acme.","dataset":"other"}`), 200)

	rec = do("GET", "/v1/gb/datasets/main/graph?user_id=u1", "")
	want(rec, 200)
	if ct := rec.Header().Get("content-type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var g struct {
		Dataset  string `json:"dataset"`
		Entities []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"entities"`
		Edges []struct {
			SourceID int64  `json:"source_id"`
			TargetID int64  `json:"target_id"`
			Source   string `json:"source"`
			Target   string `json:"target"`
			Relation string `json:"relation"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	if g.Dataset != "main" || len(g.Entities) != 2 || len(g.Edges) != 1 {
		t.Fatalf("graph: %s", rec.Body)
	}
	e := g.Edges[0]
	ids := map[int64]string{}
	for _, n := range g.Entities {
		ids[n.ID] = n.Name
	}
	if e.Relation != "works_at" || ids[e.SourceID] != "Alice" || ids[e.TargetID] != "Acme" || e.Source != "Alice" || e.Target != "Acme" {
		t.Errorf("edge: %+v", e)
	}

	// Datasets are isolated per user.
	want(do("GET", "/v1/gb/datasets/other/graph?user_id=u1", ""), 404)
	want(do("GET", "/v1/gb/datasets/other/graph?user_id=u2", ""), 200)

	// Soft-deleted datasets cannot be exported.
	want(do("DELETE", "/v1/gb/datasets/main?user_id=u1", ""), 204)
	want(do("GET", "/v1/gb/datasets/main/graph?user_id=u1", ""), 404)
}
