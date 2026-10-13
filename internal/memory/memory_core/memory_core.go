// Package memory_core holds what the pipelines of internal/memory (the text pipeline in package memory_remember, the image
// pipeline in memory_image) have in common: the LLM interface and its call recording, the per-call options, the ATIF
// trace of a call, user and dataset name validation, and JSON extraction from LLM output.
package memory_core

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"

	log "github.com/sirupsen/logrus"
)

//---------------------------------------------------------------------------------------------

// LLM is the subset of ai.Client used here.
type LLM interface {
	Complete(ctx context.Context, req ai.Request) (*ai.Response, error)
}

//---------------------------------------------------------------------------------------------

// recordingLLM wraps an LLM and stores a gb_llm_calls row (tokens, cost, call type) for every successful call.
// Calls are recorded as they happen, so cost is kept even if a later step of the operation fails.
type recordingLLM struct {
	LLM
	db   *sql.DB
	kind string
}

// WithRecording returns llm with every successful call stored in gb_llm_calls as a call of the given kind.
func WithRecording(llm LLM, d *sql.DB, kind string) LLM {
	return &recordingLLM{LLM: llm, db: d, kind: kind}
}

func (r *recordingLLM) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	resp, err := r.LLM.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	// Detached from ctx so a cancelled request still records money that was spent.
	if err := RecordCall(context.WithoutCancel(ctx), r.db, r.kind, resp.Provider, resp.Model, resp.InputTokens, resp.OutputTokens, resp.CostUSD); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"kind":          r.kind,
			"provider":      resp.Provider,
			"model":         resp.Model,
			"input_tokens":  resp.InputTokens,
			"output_tokens": resp.OutputTokens,
			"cost_usd":      resp.CostUSD,
		}).Error("record llm call")
		return resp, nil
	}
	log.WithFields(log.Fields{
		"kind":          r.kind,
		"provider":      resp.Provider,
		"model":         resp.Model,
		"input_tokens":  resp.InputTokens,
		"output_tokens": resp.OutputTokens,
		"cost_usd":      resp.CostUSD,
	}).Debug("llm call recorded")
	return resp, nil
}

// RecordCall stores one LLM/embedding API call.
func RecordCall(ctx context.Context, d *sql.DB, kind, provider, model string, inputTokens, outputTokens int, costUSD float64) error {
	return relational.RecordLLMCall(ctx, d, kind, provider, model, inputTokens, outputTokens, costUSD)
}

//---------------------------------------------------------------------------------------------

// JSONObject returns the outermost {...} in s, tolerating surrounding prose or code fences.
func JSONObject(s string) (string, error) {
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return "", errors.New("memory: no JSON object in LLM output")
	}
	return s[start : end+1], nil
}
