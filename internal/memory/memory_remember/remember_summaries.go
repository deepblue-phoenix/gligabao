package memory_remember

// Chunk summaries (see docs/chunking.md#chunk-summaries): every chunk of a long text is summarized by an LLM call (the
// summarize_chunk stage). The summary is stored as an artifact of its own, of kind chunk_summary, a child of the chunk,
// that records the model that wrote it and the reasoning level it ran with, and it is embedded into the dataset's
// summary index. The graph is not extracted from the summary: it is extracted from the chunk.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// summaryMaxTokens is the largest summary (the reasoning budget, if any, comes on top of it).
const summaryMaxTokens = 512

// chunkSummary is the summary of a chunk, before it is stored.
type chunkSummary struct {
	Text string
	// Provider and Model name the model that wrote it (as the LLM response reports them), and ModelID is its ID in gb_models
	// (relational.ModelID(Model)); store registers the model if it is not stored yet.
	Provider, Model, ModelID string
	// Reasoning is the reasoning level it ran with: "none", "low", "medium" or "high".
	Reasoning string
}

// SummaryResult is the summary of a chunk as stored: an artifact of its own (a child of the chunk).
type SummaryResult struct {
	ArtifactID int64  `json:"artifact_id"`
	Text       string `json:"text"`
	// Model is the model that wrote the summary, as the LLM API reported it, and Reasoning the reasoning level it ran with
	// ("none" if it did not reason). Both are stored on the artifact (gb_artifacts.model_id, gb_artifacts.reasoning).
	Model     string `json:"model"`
	Reasoning string `json:"reasoning"`
}

// summarizeChunk asks the LLM for a short summary of text, a chunk of a longer one. reasoning is the level the call is made with
// (ai.Reasoning*, "" for none); llm must be the one of the summarize_chunk stage, which applies it (see
// memory_core.CallOptions.ModelLLM): reasoning is only recorded here.
func summarizeChunk(ctx context.Context, llm memory_core.LLM, text, reasoning string) (*chunkSummary, error) {
	level, ok := ai.NormalizeReasoning(reasoning)
	if !ok {
		return nil, fmt.Errorf("memory: unknown reasoning level %q", reasoning)
	}
	resp, err := llm.Complete(ctx, ai.Request{
		System:    summarySystemPrompt,
		Messages:  []ai.Message{{Role: ai.RoleUser, Content: "<text>\n" + text + "\n</text>"}},
		MaxTokens: summaryMaxTokens,
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return nil, errors.New("memory: LLM summary truncated")
	}
	s := strings.TrimSpace(resp.Text)
	if s == "" {
		return nil, errors.New("memory: LLM returned an empty summary")
	}
	return &chunkSummary{Text: s, Provider: resp.Provider, Model: resp.Model, ModelID: relational.ModelID(resp.Model), Reasoning: level}, nil
}
