package api

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gligabao-atif-*")
	if err != nil {
		panic(err)
	}
	memory_core.TrajectoryDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// mgrFor makes the remember manager for Register, which stops with the test.
func mgrFor(t testing.TB, database *db.DB, llm memory_core.LLM) *memory_remember.Manager {
	return memory_remember.NewManager(t.Context(), database.Relational, llm)
}

// fakeEmbedder embeds every text as [1, 0] and reports no tokens and no cost: Register needs an embedder, which both
// remember (to embed what it stores) and query (to embed the question) use.
type fakeEmbedder struct{}

func (fakeEmbedder) EmbedBatchUsage(_ context.Context, texts []string) ([][]float32, *ai.EmbedUsage, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, &ai.EmbedUsage{Provider: ai.ProviderOpenAI, Model: "test-embed"}, nil
}

// withVectors returns a database over d, with a vector store in memory (2 dimensions, like fakeEmbedder's).
func withVectors(t testing.TB, d *sql.DB) *db.DB {
	t.Helper()
	vs, err := vector.NewChromem(":memory:", 2)
	if err != nil {
		t.Fatal(err)
	}
	return &db.DB{Relational: d, Graph: d, Vector: vs}
}
