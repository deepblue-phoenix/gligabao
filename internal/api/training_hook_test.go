package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

func TestClassifyingTrainingHook(t *testing.T) { dbtest.Run(t, testClassifyingTrainingHook) }

// POST /v1/gb/datasets/{name}/training gives the hook every artifact of the dataset with its graph, and answers with a summary of the
// (possibly filtered) training dataset the hook returns, checking that it is valid.
func testClassifyingTrainingHook(t *testing.T, d *sql.DB) {
	var got []memory_core.KnowledgeDataset
	var mode string
	hook := func(_ context.Context, ds memory_core.KnowledgeDataset) (memory_core.TrainingDataset, error) {
		got = append(got, ds)
		td := memory_core.TrainingDataset{KnowledgeDataset: ds, Classes: map[int64]string{}}
		switch mode {
		case "fail":
			return memory_core.TrainingDataset{}, errors.New("trainer down")
		case "panic":
			panic("hook bug")
		case "foreign":
			td.Graphs = append(td.Graphs[:len(td.Graphs):len(td.Graphs)], memory_core.ArtifactGraph{ArtifactID: 9999})
			for _, g := range td.Graphs {
				td.Classes[g.ArtifactID] = "x"
			}
			return td, nil
		case "unclassified":
			return td, nil
		case "subset": // only the first artifact, without graphs of the others
			td.Artifacts = ds.Artifacts[:1]
			td.Graphs = nil
			for _, g := range ds.Graphs {
				if g.ArtifactID == ds.Artifacts[0].ID {
					td.Graphs = append(td.Graphs, g)
					td.Classes[g.ArtifactID] = "employee"
				}
			}
			return td, nil
		}
		for _, g := range ds.Graphs {
			td.Classes[g.ArtifactID] = "employee"
		}
		return td, nil
	}

	database := &db.DB{Relational: d, Graph: d}
	mux := http.NewServeMux()
	Register(mux, mgrFor(t, database, echoLLM{}), database, echoLLM{}, nil, nil, nil, nil, WithClassifyingTrainingHook(hook))
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
	const url = "/v1/gb/datasets/main/training"

	want(do("POST", url, `{"user_id":"u1"}`), 404) // no such dataset
	want(do("POST", url, `{}`), 400)               // user_id required
	want(do("POST", url, `nope`), 400)

	want(do("POST", "/v1/gb/remember", `{"user_id":"u1","text":"Alice works at Acme."}`), 200)
	want(do("POST", "/v1/gb/remember", `{"user_id":"u1","text":"Alice works at Acme, again."}`), 200)
	want(do("POST", "/v1/gb/remember", `{"user_id":"u2","text":"Alice works at Acme.","dataset":"other"}`), 200)

	type summary struct {
		Dataset        string
		Artifacts      int
		ArtifactsTotal int `json:"artifacts_total"`
		Graphs         int
		GraphsTotal    int `json:"graphs_total"`
		Classes        map[string]string
		ClassCounts    map[string]int `json:"class_counts"`
	}
	run := func() summary {
		t.Helper()
		got = nil
		rec := do("POST", url, `{"user_id":"u1"}`)
		want(rec, 200)
		var s summary
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// The whole dataset: both artifacts of u1's dataset (not u2's), each with its graph.
	s := run()
	if len(got) != 1 || len(got[0].Artifacts) != 2 || len(got[0].Graphs) != 2 || got[0].UserID != "u1" || got[0].Dataset != "main" {
		t.Fatalf("the hook got %+v", got)
	}
	for _, g := range got[0].Graphs {
		if len(g.Entities) != 2 || len(g.Edges) != 1 {
			t.Errorf("graph of artifact %d: %d entities, %d edges, want 2 and 1", g.ArtifactID, len(g.Entities), len(g.Edges))
		}
	}
	if s.Dataset != "main" || s.Artifacts != 2 || s.ArtifactsTotal != 2 || s.Graphs != 2 || s.GraphsTotal != 2 || s.ClassCounts["employee"] != 2 || len(s.Classes) != 2 {
		t.Errorf("summary %+v", s)
	}

	// A filtered subset.
	mode = "subset"
	s = run()
	if s.Artifacts != 1 || s.ArtifactsTotal != 2 || s.Graphs != 1 || s.GraphsTotal != 2 || s.ClassCounts["employee"] != 1 {
		t.Errorf("subset summary %+v", s)
	}

	// A hook that fails, panics or makes an invalid dataset is a 502.
	for _, m := range []string{"fail", "panic", "foreign", "unclassified"} {
		mode = m
		want(do("POST", url, `{"user_id":"u1"}`), 502)
	}

	// Without the hook there is no route.
	mux = http.NewServeMux()
	Register(mux, mgrFor(t, database, echoLLM{}), database, echoLLM{}, nil, nil, nil, nil)
	want(do("POST", url, `{"user_id":"u1"}`), 404)
}
