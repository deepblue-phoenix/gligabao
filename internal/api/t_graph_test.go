package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestGraphExport(t *testing.T) { dbtest.Run(t, testGraphExport) }

func testGraphExport(t *testing.T,
	d *sql.DB) {
	mux := http.NewServeMux()
	Register(mux,
		mgrFor(t,
			&db.DB{
				Relational: d,
				Graph:      d,
			},
			echoLLM{}),
		&db.DB{
			Relational: d,
			Graph:      d,
		},
		echoLLM{},
		nil,
		nil,
		nil,
		nil)
	do := func(method,
		path,
		body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, newReq(method,
			path,
			strings.NewReader(body)))
		return rec
	}
	want := func(rec *httptest.ResponseRecorder,
		code int) {
		t.Helper()
		if rec.Code != code {
			t.Fatalf("status %d, want %d: %s",
				rec.Code,
				code,
				rec.Body)
		}
	}

	want(do("GET",
		"/v1/gb/datasets/main/graph",
		""), 400) // user_id required
	want(do("GET",
		"/v1/gb/datasets/main/graph?user_id=u1",
		""), 404) // no such dataset
	want(do("GET",
		"/v1/gb/datasets/a%20b/graph?user_id=u1",
		""), 404)

	// An empty dataset exports empty arrays, not null.
	want(do("POST",
		"/v1/gb/datasets",
		`{"user_id":"u1","name":"empty"}`), 201)
	rec := do("GET",
		"/v1/gb/datasets/empty/graph?user_id=u1",
		"")
	want(rec, 200)
	if !strings.Contains(rec.Body.String(), `"entities":[]`) || !strings.Contains(rec.Body.String(), `"edges":[]`) {
		t.Errorf("empty graph: %s", rec.Body)
	}

	want(do("POST",
		"/v1/gb/remember",
		`{"user_id":"u1","text":"Alice works at Acme."}`), 200)
	want(do("POST",
		"/v1/gb/remember",
		`{"user_id":"u2","text":"Alice works at Acme.","dataset":"other"}`), 200)

	rec = do("GET",
		"/v1/gb/datasets/main/graph?user_id=u1",
		"")
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
	want(do("GET",
		"/v1/gb/datasets/other/graph?user_id=u1",
		""), 404)
	want(do("GET",
		"/v1/gb/datasets/other/graph?user_id=u2",
		""), 200)

	// Soft-deleted datasets cannot be exported.
	want(do("DELETE",
		"/v1/gb/datasets/main?user_id=u1",
		""), 204)
	want(do("GET",
		"/v1/gb/datasets/main/graph?user_id=u1",
		""), 404)
}

func TestArtifactGraph(t *testing.T) { dbtest.Run(t, testArtifactGraph) }

func testArtifactGraph(t *testing.T,
	d *sql.DB) {
	mux := http.NewServeMux()
	Register(mux,
		mgrFor(t,
			&db.DB{
				Relational: d,
				Graph:      d,
			},
			echoLLM{}),
		&db.DB{
			Relational: d,
			Graph:      d,
		},
		echoLLM{},
		nil,
		nil,
		nil,
		nil)
	do := func(method,
		path,
		body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, newReq(method,
			path,
			strings.NewReader(body)))
		return rec
	}
	want := func(rec *httptest.ResponseRecorder,
		code int) {
		t.Helper()
		if rec.Code != code {
			t.Fatalf("status %d, want %d: %s",
				rec.Code,
				code,
				rec.Body)
		}
	}
	remember := func(user,
		text string) int64 {
		rec := do("POST",
			"/v1/gb/remember",
			fmt.Sprintf(`{"user_id":%q,"text":%q}`, user, text))
		want(rec, 200)
		var res struct {
			ArtifactID int64 `json:"artifact_id"`
			Chunks     []struct {
				ArtifactID int64 `json:"artifact_id"`
			} `json:"chunks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res.ArtifactID
	}
	type artifactGraph struct {
		ArtifactID int64 `json:"artifact_id"`
		Entities   []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"entities"`
		Edges []struct {
			SourceID int64  `json:"source_id"`
			TargetID int64  `json:"target_id"`
			Relation string `json:"relation"`
		} `json:"edges"`
	}
	get := func(path string) artifactGraph {
		rec := do("GET",
			path,
			"")
		want(rec, 200)
		var g artifactGraph
		if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
			t.Fatal(err)
		}
		return g
	}

	// A single artifact, and a second one whose duplicate of the fact was merged: both show the one edge.
	first := remember("u1", "Alice works at Acme.")
	second := remember("u1", "Alice still works at Acme.")
	for _, id := range []int64{first, second} {
		g := get(fmt.Sprintf("/v1/gb/datasets/main/artifacts/%d/graph?user_id=u1", id))
		if g.ArtifactID != id || len(g.Entities) != 2 || len(g.Edges) != 1 || g.Edges[0].Relation != "works_at" {
			t.Fatalf("artifact %d graph: %+v", id, g)
		}
		ids := map[int64]bool{}
		for _, n := range g.Entities {
			ids[n.ID] = true
		}
		if !ids[g.Edges[0].SourceID] || !ids[g.Edges[0].TargetID] {
			t.Errorf("artifact %d: edge endpoints are not among the entities: %+v", id, g)
		}
	}

	// A long text is chunked: the parent has no links of its own, its graph is that of its chunks (unless asked not to).
	long := remember("u1", strings.Repeat("Alice talked at length about her work at Acme, the river and the summer holidays to come.\n\n", 200))
	g := get(fmt.Sprintf("/v1/gb/datasets/main/artifacts/%d/graph?user_id=u1", long))
	if len(g.Entities) != 2 || len(g.Edges) != 1 {
		t.Errorf("long text graph with chunks: %+v", g)
	}
	g = get(fmt.Sprintf("/v1/gb/datasets/main/artifacts/%d/graph?user_id=u1&include_children=false", long))
	if len(g.Entities) != 0 || len(g.Edges) != 0 {
		t.Errorf("long text graph without chunks: %+v", g)
	}

	// Errors.
	base := fmt.Sprintf("/v1/gb/datasets/main/artifacts/%d/graph", first)
	want(do("GET", base, ""), 400)                                                    // user_id required
	want(do("GET", base+"?user_id=u1&include_children=x", ""), 400)                   // bad bool
	want(do("GET", "/v1/gb/datasets/main/artifacts/abc/graph?user_id=u1", ""), 400)   // bad id
	want(do("GET", "/v1/gb/datasets/main/artifacts/99999/graph?user_id=u1", ""), 404) // no such artifact
	want(do("GET", base+"?user_id=u2", ""), 404)                                      // other user: no dataset
	want(do("GET", "/v1/gb/datasets/nope/artifacts/1/graph?user_id=u1", ""), 404)     // no such dataset
	other := remember("u2", "Bob works at Initech.")
	want(do("GET", fmt.Sprintf("/v1/gb/datasets/main/artifacts/%d/graph?user_id=u1", other), ""), 404) // another dataset's artifact
}
