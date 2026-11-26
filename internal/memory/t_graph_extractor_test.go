package memory_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

func TestGraphExtractorHook(t *testing.T) { dbtest.Run(t, testGraphExtractorHook) }

// The graph extractor hook gets the text and its content type; when it handles the request the LLM does not extract, and when it passes the
// LLM extracts as usual.
func testGraphExtractorHook(t *testing.T,
	d *sql.DB) {
	ctx := context.Background()
	var got []memory_core.GraphExtractRequest
	pass := false
	hook := memory_core.WithGraphExtractor(func(_ context.Context,
		req memory_core.GraphExtractRequest) (memory_core.GraphExtractResult, error) {
		got = append(got, req)
		if pass {
			return memory_core.GraphExtractResult{Pass: true}, nil
		}
		return memory_core.GraphExtractResult{
			Entities: []graph.Entity{{Name: "main", Type: "function"}, {Name: "run", Type: "function"}},
			Edges:    []graph.Edge{{Source: "main", Target: "run", Relation: "calls"}},
		}, nil
	})

	// handled: no LLM call
	llm1 := &personLLM{}
	res, err := memory_remember.Remember(ctx,
		d,
		llm1,
		"u1",
		"code",
		"knowledge",
		"func main() { run() }",
		relational.Now(),
		hook,
		memory_core.WithContentType(memory_core.ContentCode))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ContentType != memory_core.ContentCode || got[0].Text != "func main() { run() }" || got[0].UserID != "u1" ||
		got[0].Dataset != "code" || got[0].Chunks != 1 {
		t.Fatalf("hook request = %+v", got)
	}
	if llm1.calls != 0 {
		t.Errorf("the LLM was called %d times although the hook handled the request", llm1.calls)
	}
	if len(res.Entities) != 2 || len(res.Edges) != 1 || res.Edges[0].Relation != "calls" || res.Edges[0].Description == "" {
		t.Errorf("result = %+v", res)
	}

	// passed: the LLM extracts; no content type means text
	pass = true
	llm2 := &personLLM{}
	res, err = memory_remember.Remember(ctx,
		d,
		llm2,
		"u1",
		"code",
		"knowledge",
		"Person1 works at Acme.",
		relational.Now(),
		hook)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].ContentType != memory_core.ContentText {
		t.Errorf("hook requests = %+v", got)
	}
	if llm2.calls == 0 || len(res.Entities) == 0 {
		t.Errorf("the LLM did not extract after the hook passed: calls %d, result %+v", llm2.calls, res)
	}
}
