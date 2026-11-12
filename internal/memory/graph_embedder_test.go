package memory_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

func TestGraphEmbedderHook(t *testing.T) { dbtest.Run(t, testGraphEmbedderHook) }

// The graph embedder hook is called once with the fully constructed graph of a remember (with the IDs of its run and artifact) and once
// with the graph context of a query; its result is not used, and its failure (or panic) does not fail the call.
func testGraphEmbedderHook(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	var mu sync.Mutex
	var reqs []memory_core.GraphEmbedRequest
	var fail, boom bool
	index := ""
	hook := memory_core.WithGraphEmbedder(func(_ context.Context,
		req memory_core.GraphEmbedRequest) ([]float32, error) {
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, req)
		if req.Index != index {
			t.Errorf("the request has the index %q, want %q",
				req.Index,
				index)
		}
		if boom {
			panic("hook bug")
		}
		if fail {
			return nil, errors.New("model down")
		}
		return []float32{0.1, 0.2, 0.3}, nil
	})
	vopt := withVectors(t)

	// Without the hook nothing is called.
	if _, err := memory_remember.Remember(ctx,
		d,
		&personLLM{},
		"u1",
		"work",
		"episode",
		"Person1 works at Acme.",
		relational.Now(),
		vopt); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 0 {
		t.Fatalf("hook called without being set: %+v", reqs)
	}

	res, err := memory_remember.Remember(ctx,
		d,
		&personLLM{},
		"u1",
		"work",
		"episode",
		"Person2 works at Acme.",
		relational.Now(),
		vopt,
		hook)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("remember called the hook %d times, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Pipeline != memory_core.GraphEmbedRemember || r.UserID != "u1" || r.Dataset != "work" || r.RunID != res.RememberRunID || r.RunID == 0 ||
		r.ArtifactID != res.ArtifactID || len(r.Entities) == 0 || len(r.Edges) != len(res.Edges) || len(r.Edges) == 0 {
		t.Errorf("remember request = %+v, result = %+v",
			r,
			res)
	}
	for _, e := range r.Entities {
		if e.ID == 0 || e.Name == "" {
			t.Errorf("entity %+v is not a stored entity", e)
		}
	}

	index = "small"
	if _, err := memory_remember.Remember(ctx,
		d,
		&personLLM{},
		"u1",
		"work",
		"episode",
		"Person4 works at Acme.",
		relational.Now(),
		vopt,
		hook,
		memory_core.WithGraphIndex(index)); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || reqs[1].Index != "small" {
		t.Fatalf("a remember with an index: requests = %+v", reqs)
	}
	index = ""

	reqs = nil
	question := "Where does Person1 work?"
	ans, err := memory_query.Query(ctx,
		d,
		answerLLM{},
		"u1",
		"work",
		question,
		vopt,
		hook)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("query called the hook %d times, want 1", len(reqs))
	}
	r = reqs[0]
	if r.Pipeline != memory_core.GraphEmbedQuery || r.Question != question || r.RunID != ans.QueryRunID || r.RunID == 0 || len(r.Entities) == 0 {
		t.Errorf("query request = %+v", r)
	}

	// A failing or panicking hook does not fail the calls.
	fail = true
	if _, err := memory_query.Query(ctx,
		d,
		answerLLM{},
		"u1",
		"work",
		question,
		vopt,
		hook); err != nil {
		t.Errorf("query with a failing hook: %v", err)
	}
	fail, boom = false, true
	if _, err := memory_remember.Remember(ctx,
		d,
		&personLLM{},
		"u1",
		"work",
		"episode",
		"Person3 works at Acme.",
		relational.Now(),
		vopt,
		hook); err != nil {
		t.Errorf("remember with a panicking hook: %v", err)
	}
}
