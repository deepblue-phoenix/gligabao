package memory_core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
)

// The stages of remember that call an LLM. A call can choose the model of each one (WithModels); a stage it names no
// model for uses the default model of the LLM client.
const (
	// StageExtractGraph is the extraction of the entities and edges from a text (or from each chunk of a long one). Its model
	// is the one recorded on the entities and edges it yields (their model_id).
	StageExtractGraph = "extract_graph"
	// StageSummarizeChunk is the summary of each chunk of a long text (see docs/chunking.md). It is the only stage that
	// also takes a reasoning level (WithReasoning); the model and the level are recorded on the summary artifact.
	StageSummarizeChunk = "summarize_chunk"
	// StageResolveEdges is the comparison of the new edges with the stored ones (duplicates, contradictions).
	StageResolveEdges = "resolve_edges"
	// StageExtractImage is the reading of an image (the image pipeline).
	StageExtractImage = "extract_image"
	// StageWebSearch is the web search made for the key entities of a text (only with the web_search flag of remember,
	// see docs/web-search.md). The model must support Anthropic's web search tool. The entities and edges found in the
	// results are extracted on the extract_graph model, like any other text.
	StageWebSearch = "web_search"
	// StageResolveSeries is the decision of which timeseries provider to fetch for which entity of a text (only with
	// timeseries.fetch, see docs/timeseries.md): the LLM reads the providers' descriptions.
	StageResolveSeries = "resolve_series"
)

// Stages lists the stages that take a model, in pipeline order.
var Stages = []string{StageExtractGraph, StageSummarizeChunk, StageResolveEdges, StageWebSearch, StageResolveSeries, StageExtractImage}

// MaxModelLen is the maximum length of a model name in bytes.
const MaxModelLen = 128

// ErrInvalidModels means a stage name is unknown or a model name is malformed.
var ErrInvalidModels = errors.New("invalid models")

// Models maps a stage of remember to the model that runs it. A stage that is missing uses the default model.
type Models map[string]string

// ResolveModels validates m and returns a normalized copy: model names are trimmed, and a stage with an empty model is
// dropped (it means the default). It returns ErrInvalidModels for an unknown stage, or a model name that is too long or
// has whitespace or control characters. A nil or empty m gives nil. Whether the provider knows the model is not checked
// here: an unknown one makes the LLM call fail.
func ResolveModels(m map[string]string) (Models, error) {
	var out Models
	for stage, model := range m {
		if !validStage(stage) {
			return nil, fmt.Errorf("%w: unknown stage %q (stages: %s)", ErrInvalidModels, stage, strings.Join(Stages, ", "))
		}
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if len(model) > MaxModelLen {
			return nil, fmt.Errorf("%w: %s: model name is longer than %d bytes", ErrInvalidModels, stage, MaxModelLen)
		}
		for _, r := range model {
			if r <= 0x20 || r == 0x7f {
				return nil, fmt.Errorf("%w: %s: model name has whitespace or control characters", ErrInvalidModels, stage)
			}
		}
		if out == nil {
			out = Models{}
		}
		out[stage] = model
	}
	return out, nil
}

func validStage(s string) bool {
	for _, k := range Stages {
		if s == k {
			return true
		}
	}
	return false
}

// Merge returns the models of m with those of over on top: a stage over names replaces m's. Neither is changed.
func (m Models) Merge(over Models) Models {
	if len(m) == 0 && len(over) == 0 {
		return nil
	}
	out := Models{}
	for k, v := range m {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// String lists the stages that have a model, as "extract_graph=a,resolve_edges=b" in pipeline order ("" for none).
func (m Models) String() string {
	var parts []string
	for _, s := range Stages {
		if v := m[s]; v != "" {
			parts = append(parts, s+"="+v)
		}
	}
	return strings.Join(parts, ",")
}

// ParseModels parses the list form "extract_graph=a,resolve_edges=b" (the inverse of String) and validates it with ResolveModels.
// An empty s gives nil.
func ParseModels(s string) (Models, error) {
	m := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		stage, model, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("%w: %q is not stage=model", ErrInvalidModels, part)
		}
		m[strings.TrimSpace(stage)] = model
	}
	return ResolveModels(m)
}

// WithModels sets the model of each LLM stage of Remember and RememberImage (see the Stage constants; web_search is the model of the web search). A stage that is
// not in m, or whose model is empty, uses the default model. Validate m with ResolveModels first.
func WithModels(m Models) Option { return func(o *CallOptions) { o.Models = m } }

// ModelLLM returns llm with the model (and, for the stages that take one, the reasoning level) of the given stage set on
// every request that names none. With neither it returns llm itself, so the client's default model is used.
func (o CallOptions) ModelLLM(llm LLM, stage string) LLM {
	model, reasoning := o.Models[stage], o.Reasoning[stage]
	if model == "" && reasoning == "" {
		return llm
	}
	return &modelLLM{LLM: llm, model: model, reasoning: reasoning}
}

type modelLLM struct {
	LLM
	model, reasoning string
}

func (m *modelLLM) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if req.Model == "" {
		req.Model = m.model
	}
	if req.Reasoning == "" {
		req.Reasoning = m.reasoning
	}
	return m.LLM.Complete(ctx, req)
}

//---------------------------------------------------------------------------------------------

// ReasoningStages lists the stages that take a reasoning level.
var ReasoningStages = []string{StageSummarizeChunk}

// ErrInvalidReasoning means a stage that takes no reasoning level is named, or a level is unknown.
var ErrInvalidReasoning = errors.New("invalid reasoning")

// Reasoning maps a stage (see ReasoningStages) to its reasoning level (ai.ReasoningLow, ...). A stage that is missing
// runs without reasoning.
type Reasoning map[string]string

// ResolveReasoning validates m and returns a normalized copy: levels are trimmed and lowercased, and a stage with an
// empty level or "none" is dropped (no reasoning). It returns ErrInvalidReasoning for a stage that takes no reasoning
// level or an unknown level. A nil or empty m gives nil.
func ResolveReasoning(m map[string]string) (Reasoning, error) {
	var out Reasoning
	for stage, level := range m {
		ok := false
		for _, s := range ReasoningStages {
			ok = ok || s == stage
		}
		if !ok {
			return nil, fmt.Errorf("%w: unknown stage %q (stages with a reasoning level: %s)", ErrInvalidReasoning, stage, strings.Join(ReasoningStages, ", "))
		}
		l, valid := ai.NormalizeReasoning(level)
		if !valid {
			return nil, fmt.Errorf("%w: %s: unknown level %q (levels: %s)", ErrInvalidReasoning, stage, level, strings.Join(ai.ReasoningLevels, ", "))
		}
		if l == ai.ReasoningNone {
			continue
		}
		if out == nil {
			out = Reasoning{}
		}
		out[stage] = l
	}
	return out, nil
}

// WithReasoning sets the reasoning level of the stages that take one (see ReasoningStages). A stage that is not in r
// runs without reasoning. Validate r with ResolveReasoning first.
func WithReasoning(r Reasoning) Option { return func(o *CallOptions) { o.Reasoning = r } }

// Level is the reasoning level of a stage as it is recorded: "none" if the stage has none.
func (r Reasoning) Level(stage string) string {
	if l := r[stage]; l != "" {
		return l
	}
	return ai.ReasoningNone
}
