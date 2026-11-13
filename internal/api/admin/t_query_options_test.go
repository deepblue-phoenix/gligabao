package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
)

// TestQueryOptionsOf checks that the query modal is offered the LLM models, sorted and without the embedding ones, the default model and
// the thinking levels, as lists (never null).
func TestQueryOptionsOf(t *testing.T) {
	h := admin.QueryOptionsOf([]llm.ModelInfo{
		{
			Name: "zeta",
			Kind: llm.KindLLM,
		}, {
			Name: "emb",
			Kind: llm.KindEmbedding,
		}, {
			Name: "alpha",
			Kind: llm.KindLLM,
		},
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet,
		"/x",
		nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s",
			rec.Code,
			rec.Body)
	}
	var got struct {
		Models       []string `json:"models"`
		DefaultModel string   `json:"default_model"`
		Reasoning    []string `json:"reasoning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Models, []string{"alpha", "zeta"}) {
		t.Errorf("models %v, want [alpha zeta]", got.Models)
	}
	if got.DefaultModel != llm.DefaultModel() || !slices.Equal(got.Reasoning, llm.ReasoningLevels) {
		t.Errorf("default %q, reasoning %v",
			got.DefaultModel,
			got.Reasoning)
	}
	rec = httptest.NewRecorder()
	admin.QueryOptionsOf(nil)(rec, httptest.NewRequest(http.MethodGet,
		"/x",
		nil))
	var empty map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil || empty["models"] == nil {
		t.Errorf("no models must be [], got %s", rec.Body)
	}
}
