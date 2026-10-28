package query

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
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

// topicEmbedder embeds a text by its topic, on 5 dimensions: employment, engineering, walks, a question without a topic, and
// everything else (so that texts about other things are far from all the topics the tests ask about, and a question that has
// none is far from every text). Questions use the same words as what they
// should find only by topic ("employer" for "works at"), not by keyword.
type topicEmbedder struct{}

func (topicEmbedder) EmbedBatchUsage(_ context.Context, texts []string) ([][]float32, *ai.EmbedUsage, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		t = strings.ToLower(t)
		switch {
		case strings.Contains(t, "employ") || strings.Contains(t, "work"):
			out[i] = []float32{1, 0, 0, 0, 0}
		case strings.Contains(t, "profession") || strings.Contains(t, "engineer"):
			out[i] = []float32{0, 1, 0, 0, 0}
		case strings.Contains(t, "outdoor") || strings.Contains(t, "walk"):
			out[i] = []float32{0, 0, 1, 0, 0}
		case strings.Contains(t, "?"):
			out[i] = []float32{0, 0, 0, 1, 0}
		default:
			out[i] = []float32{0, 0, 0, 0, 1}
		}
	}
	return out, &ai.EmbedUsage{Provider: ai.ProviderOpenAI, Model: "test-embed", InputTokens: len(texts)}, nil
}

// withVectors returns the option that gives a call a vector store of its own (in memory) and the topicEmbedder. The
// same option shared by remember and query makes query see what remember embedded.
func withVectors(t testing.TB) memory_core.Option {
	t.Helper()
	vs, err := vector.NewChromem(":memory:", 5)
	if err != nil {
		t.Fatal(err)
	}
	return memory.WithVectors(vs, topicEmbedder{})
}
