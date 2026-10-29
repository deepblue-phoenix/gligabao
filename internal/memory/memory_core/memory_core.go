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

// WebSearcher runs web searches (see ai.Client.WebSearch and docs/web-search.md). It is the Anthropic web search tool, so
// it exists only when an Anthropic API key is supplied; *ai.Client implements it.
type WebSearcher interface {
	WebSearch(ctx context.Context, req ai.WebSearchRequest) (*ai.WebSearchResponse, error)
}

//---------------------------------------------------------------------------------------------

// recordingLLM wraps an LLM and stores a gb_inferences row (model, thinking level, tokens, cost) for every successful call.
// Runs are recorded as the calls happen, so cost is kept even if a later step of the operation fails.
type recordingLLM struct {
	LLM
	db   *sql.DB
	kind string
}

// WithRecording returns llm with every successful call stored in gb_inferences as a run of an operation of the given
// kind (the type of the run). The stage comes from the context (see WithStage) and the thinking level is the one the call
// ran with (ai.Response.Reasoning, or the request's if the LLM does not report it). The ID of the run is returned in
// ai.Response.InferenceID.
func WithRecording(llm LLM, d *sql.DB, kind string) LLM {
	return &recordingLLM{LLM: llm, db: d, kind: kind}
}

func (r *recordingLLM) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	resp, err := r.LLM.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	level := resp.Reasoning
	if level == "" {
		level, _ = ai.NormalizeReasoning(req.Reasoning)
	}
	if level == "" {
		level = ai.ReasoningNone
	}
	run := relational.Inference{
		Type: r.kind, Stage: StageOf(ctx), Model: resp.Model, Provider: resp.Provider, Kind: ai.KindOf(resp.Model), Reasoning: level,
		InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens, CostUSD: resp.CostUSD,
	}
	// Detached from ctx so a cancelled request still records money that was spent.
	id, err := RecordRun(context.WithoutCancel(ctx), r.db, run)
	fields := log.Fields{
		"kind":          r.kind,
		"stage":         run.Stage,
		"provider":      resp.Provider,
		"model":         resp.Model,
		"reasoning":     level,
		"input_tokens":  resp.InputTokens,
		"output_tokens": resp.OutputTokens,
		"cost_usd":      resp.CostUSD,
	}
	if err != nil {
		log.WithError(err).WithFields(fields).Error("record inference")
		return resp, nil
	}
	fields["inference_id"] = id
	log.WithFields(fields).Debug("inference recorded")
	out := *resp
	out.Reasoning, out.InferenceID = level, id
	return &out, nil
}

// RecordRun stores one invocation of a model (see relational.RecordInference) and returns the ID of its run. It is what
// the calls that do not go through WithRecording (embeddings, web searches, Jev) use.
//
// If ctx carries an inference sink (see WithInferenceSink) the ID of a run that was recorded is passed to it, which is how the
// pipeline stage that made the call learns of its inference.
func RecordRun(ctx context.Context, d *sql.DB, run relational.Inference) (int64, error) {
	id, err := relational.RecordInference(ctx, d, run)
	if err == nil {
		NoteInference(ctx, id)
	}
	return id, err
}

//------------------
// MEMORY PIPELINE

// NoteInference passes the ID of a recorded inference to the inference sink of ctx (see WithInferenceSink), if it has one. 0, a
// run that could not be recorded, is ignored.
func NoteInference(ctx context.Context, id int64) {
	if sink, _ := ctx.Value(inferenceSinkKey{}).(func(int64)); sink != nil && id != 0 {
		sink(id)
	}
}

type inferenceSinkKey struct{}

// WithInferenceSink returns ctx that makes RecordRun pass the ID of every inference it records to sink, replacing the sink of
// ctx if it had one. The pipeline runner (package memory_pipeline) sets it for each stage, so that the run log of the call
// files the inferences under the stage that made them. sink may be called from several goroutines.
func WithInferenceSink(ctx context.Context, sink func(id int64)) context.Context {
	return context.WithValue(ctx, inferenceSinkKey{}, sink)
}

//------------------

//---------------------------------------------------------------------------------------------

// JSONObject returns the outermost {...} in s, tolerating surrounding prose or code fences.
func JSONObject(s string) (string, error) {
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return "", errors.New("memory: no JSON object in LLM output")
	}
	return s[start : end+1], nil
}
