// Package memory turns free text into a bi-temporal knowledge graph (modelled on Graphiti) with an LLM and stores it in the configured database (see internal/db/graph).
//
// Every input is an episode (a row in gb_memories) with an event time (ValidAt: when the text was written or
// observed) and a system time (CreatedAt: when it was ingested). The facts extracted from it are edges between
// entities; each edge carries its own event time (ValidAt / InvalidAt) and system time (CreatedAt / ExpiredAt).
// See temporal.go for how facts are reconciled with what is already stored.
package memory

import (
	"context"
	"database/sql"
	"log"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

//---------------------------------------------------------------------------------------------

// LLM is the subset of ai.Client used here.
type LLM interface {
	Complete(ctx context.Context, req ai.Request) (*ai.Response, error)
}

//---------------------------------------------------------------------------------------------

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

//---------------------------------------------------------------------------------------------

func key(name string) string { return strings.ToLower(strings.Join(strings.Fields(name), " ")) }
