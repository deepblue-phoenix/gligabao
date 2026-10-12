package memory_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
)

type extractLLM struct{}

func (extractLLM) Complete(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{StopReason: "end_turn", Text: `{"entities":[{"name":"Alice","type":"person","description":"engineer"},{"name":"Acme","type":"organization"}],
	"edges":[{"source":"Alice","target":"Acme","relation":"works at"},{"source":"Alice","target":"Paris","relation":"lives in"}]}`}, nil
}

// fakeEmbedder embeds every text as [len, 1].
type fakeEmbedder struct{ texts []string }

func (f *fakeEmbedder) EmbedBatchUsage(_ context.Context, texts []string) ([][]float32, *ai.EmbedUsage, error) {
	f.texts = append(f.texts, texts...)
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = []float32{float32(len(t)), 1}
	}
	return out, &ai.EmbedUsage{Provider: ai.ProviderOpenAI, Model: "m", InputTokens: len(texts)}, nil
}

func TestRememberEmbedsEntities(t *testing.T) { dbtest.Run(t, testRememberEmbedsEntities) }

func testRememberEmbedsEntities(t *testing.T, d *sql.DB) {
	ctx := context.Background()
	vs, err := vector.NewChromem(":memory:", 2)
	if err != nil {
		t.Fatal(err)
	}
	emb := &fakeEmbedder{}
	opt := memory.WithVectors(vs, emb)

	res, err := memory.Remember(ctx, d, extractLLM{}, "u1", "work", "", "Alice works at Acme.", relational.Now(), opt)
	if err != nil {
		t.Fatal(err)
	}
	// Entities: Alice, Acme, and Paris (created from an edge). Edges: the two facts.
	if res.EmbeddedEntities != 3 || res.EmbeddedEdges != 2 || len(emb.texts) != 5 {
		t.Fatalf("embedded %d entities, %d edges, texts %q", res.EmbeddedEntities, res.EmbeddedEdges, emb.texts)
	}
	if !strings.Contains(strings.Join(emb.texts, "|"), "Alice (person): engineer") {
		t.Errorf("texts = %q", emb.texts)
	}
	dsID, err := relational.DatasetID(ctx, d, "u1", "work")
	if err != nil {
		t.Fatal(err)
	}
	got, err := vs.Query(ctx, memory.EntityDataset(dsID), []float32{1, 1}, vector.QueryOptions{TopK: 10})
	if err != nil || len(got) != 3 {
		t.Fatalf("matches = %v, %v", got, err)
	}
	for _, m := range got {
		if m.Metadata["kind"] != memory.VectorKindEntity || m.Metadata["name"] == "" {
			t.Errorf("metadata = %v", m.Metadata)
		}
	}
	// The edges are in an index of their own, not mixed with the entities.
	if memory.EntityDataset(dsID) == memory.EdgeDataset(dsID) || !strings.HasSuffix(memory.EntityDataset(dsID), "-entities") ||
		!strings.HasSuffix(memory.EdgeDataset(dsID), "-edges") {
		t.Errorf("vector datasets: %q, %q", memory.EntityDataset(dsID), memory.EdgeDataset(dsID))
	}
	edgeMatches, err := vs.Query(ctx, memory.EdgeDataset(dsID), []float32{1, 1}, vector.QueryOptions{TopK: 10})
	if err != nil || len(edgeMatches) != 2 {
		t.Fatalf("edge matches = %v, %v", edgeMatches, err)
	}
	for _, m := range edgeMatches {
		if m.Metadata["kind"] != memory.VectorKindEdge || m.Metadata["relation"] == "" {
			t.Errorf("edge metadata = %v", m.Metadata)
		}
	}

	// Remembering again overwrites the same vectors, and another dataset gets its own vector dataset.
	if _, err := memory.Remember(ctx, d, extractLLM{}, "u1", "work", "", "again", relational.Now(), opt); err != nil {
		t.Fatal(err)
	}
	if got, _ := vs.Query(ctx, memory.EntityDataset(dsID), []float32{1, 1}, vector.QueryOptions{TopK: 10}); len(got) != 3 {
		t.Errorf("after second remember: %d vectors, want 3", len(got))
	}
	if got, _ := vs.Query(ctx, memory.EdgeDataset(dsID), []float32{1, 1}, vector.QueryOptions{TopK: 10}); len(got) != 2 {
		t.Errorf("after second remember: %d edge vectors, want 2 (the repeated facts are the same edges)", len(got))
	}
	if _, err := memory.Remember(ctx, d, extractLLM{}, "u1", "other", "", "x", relational.Now(), opt); err != nil {
		t.Fatal(err)
	}
	otherID, _ := relational.DatasetID(ctx, d, "u1", "other")
	if got, _ := vs.Query(ctx, memory.EntityDataset(otherID), []float32{1, 1}, vector.QueryOptions{TopK: 10}); len(got) != 3 {
		t.Errorf("other dataset: %d vectors, want 3", len(got))
	}

	// Without WithVectors nothing is embedded.
	n := len(emb.texts)
	if _, err := memory.Remember(ctx, d, extractLLM{}, "u1", "work", "", "plain", relational.Now()); err != nil {
		t.Fatal(err)
	}
	if len(emb.texts) != n {
		t.Error("embedded without WithVectors")
	}
}
