package memory_remember

// Chunk summaries (see docs/chunking.md#chunk-summaries): every chunk of a long text is summarized by an LLM call (the
// summarize_chunk stage). The summary is stored as an artifact of its own, of kind chunk_summary, a child of the chunk,
// that references the run (gb_inferences) of the model that wrote it, which records the model and the reasoning level it ran
// with, and it is embedded into the dataset's
// summary index. The graph is not extracted from the summary: it is extracted from the chunk.

import (
	"context"
	"errors"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// chunkSummary is the summary of a chunk, before it is stored.
type chunkSummary struct {
	Text string
	// Provider and Model name the model that wrote it (as the LLM response reports them).
	Provider, Model string
	// InferenceID is the run (gb_inferences) of the call that wrote it, 0 if it could not be recorded. The run is stored
	// as it happens, and holds the model (registered with it) and the reasoning level the call ran with.
	InferenceID int64
	// Reasoning is the reasoning level it ran with: "none", "low", "medium" or "high".
	Reasoning string
	// Raw is the text the LLM answered, kept for the stage outputs (see runLog.Output); "" if it did not answer.
	Raw string
}

// SummaryResult is the summary of a chunk as stored: an artifact of its own (a child of the chunk).
type SummaryResult struct {
	ArtifactID int64  `json:"artifact_id"`
	Text       string `json:"text"`
	// Model is the model that wrote the summary, as the LLM API reported it, and Reasoning the reasoning level it ran with
	// ("none" if it did not reason). Both are tracked by the inference of the call, which the artifact references
	// (gb_artifacts.inference_id, InferenceID here).
	Model       string `json:"model"`
	Reasoning   string `json:"reasoning"`
	InferenceID int64  `json:"inference_id,omitempty"`
}

// summaryMaxTokens is the largest summary (the thinking budget, if any, comes on top of it).
const summaryMaxTokens = 512

// RunID is the inference of the summary (0 for none, and for a nil s).
func (s *chunkSummary) RunID() int64 {
	if s == nil {
		return 0
	}
	return s.InferenceID
}

// summarizeChunk asks the LLM for a short summary of text, a chunk of a longer one. llm must be the one of the
// summarize_chunk stage, which applies its model and reasoning level (see memory_core.CallOptions.ModelLLM); the level the
// call ran with and its run are read from the response (see memory_core.WithRecording). If the LLM answered but its answer
// is no use, the error comes with a summary that is empty but for its InferenceID.
func summarizeChunk(ctx context.Context, llm memory_core.LLM, text string) (*chunkSummary, error) {
	resp, err := llm.Complete(ctx, ai.Request{
		System:    summarySystemPrompt,
		Messages:  []ai.Message{{Role: ai.RoleUser, Content: "<text>\n" + text + "\n</text>"}},
		MaxTokens: summaryMaxTokens,
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return &chunkSummary{InferenceID: resp.InferenceID, Raw: resp.Text}, errors.New("memory: LLM summary truncated")
	}
	s := strings.TrimSpace(resp.Text)
	if s == "" {
		return &chunkSummary{InferenceID: resp.InferenceID, Raw: resp.Text}, errors.New("memory: LLM returned an empty summary")
	}
	level, _ := ai.NormalizeReasoning(resp.Reasoning)
	return &chunkSummary{Text: s, Provider: resp.Provider, Model: resp.Model, InferenceID: resp.InferenceID, Reasoning: level, Raw: resp.Text}, nil
}
