package memory

import (
	"context"
	"database/sql"
	"log"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

// Call types stored in gb_llm_calls.type.
const (
	CallRemember = "remember"
	CallRecall   = "recall"
)

//---------------------------------------------------------------------------------------------

// recordingLLM wraps an LLM and stores a gb_llm_calls row (tokens, cost, call type) for every successful call.
// Calls are recorded as they happen, so cost is kept even if a later step of the operation fails.
type recordingLLM struct {
	LLM
	db   *sql.DB
	kind string
}

func withRecording(llm LLM, d *sql.DB, kind string) LLM {
	return &recordingLLM{LLM: llm, db: d, kind: kind}
}

func (r *recordingLLM) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	resp, err := r.LLM.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	// Detached from ctx so a cancelled request still records money that was spent.
	if err := RecordCall(context.WithoutCancel(ctx), r.db, r.kind, resp.Provider, resp.Model, resp.InputTokens, resp.OutputTokens, resp.CostUSD); err != nil {
		log.Printf("record llm call: %v", err)
	}
	return resp, nil
}

// RecordCall stores one LLM/embedding API call.
func RecordCall(ctx context.Context, d *sql.DB, kind, provider, model string, inputTokens, outputTokens int, costUSD float64) error {
	return relational.RecordLLMCall(ctx, d, kind, provider, model, inputTokens, outputTokens, costUSD)
}
