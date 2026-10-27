package memory_core

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	// StageDetectGeo is the decision of which entities of a text are places (a city, a country, a region, a landmark, ...), and
	// what to ask the geo resolver for each (only when a geo resolver is registered, see WithGeoResolver). Query has it too: the
	// LLM call that finds the places a question mentions.
	StageDetectGeo = "detect_geo"
)

// Stages lists the stages that take a model, in pipeline order.
var Stages = []string{StageExtractGraph, StageSummarizeChunk, StageResolveEdges, StageWebSearch, StageResolveSeries, StageDetectGeo, StageExtractImage}

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
func ResolveModels(m map[string]string) (Models, error) { return resolveModels(m, Stages) }

// QueryStages lists the stages of Query that take a model: the one that answers (StageQuery) and the detection of the places
// in the question (StageDetectGeo, only when the call has a geo resolver, see WithGeoResolver).
var QueryStages = []string{StageDetectGeo, StageQuery}

// ResolveQueryModels is ResolveModels for Query: the stages are those of QueryStages. A stage of remember that Query does not
// run is rejected, so that a typo is not silently ignored.
func ResolveQueryModels(m map[string]string) (Models, error) { return resolveModels(m, QueryStages) }

func resolveModels(m map[string]string, stages []string) (Models, error) {
	var out Models
	for stage, model := range m {
		if !slices.Contains(stages, stage) {
			return nil, fmt.Errorf("%w: unknown stage %q (stages: %s)", ErrInvalidModels, stage, strings.Join(stages, ", "))
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

// WithModels sets the model of each LLM stage of Remember, RememberImage and Query (see the Stage constants; web_search is the model of the web search; Query takes
// StageQuery and StageDetectGeo). A stage that is
// not in m, or whose model is empty, uses the default model. Validate m with ResolveModels first.
func WithModels(m Models) Option { return func(o *CallOptions) { o.Models = m } }

// ModelLLM returns llm with the model and the reasoning level of the given stage set on every request that names none, and
// the stage on the context of each call (see StageOf), so that the inference it records says which stage made it. The
// level is that of WithReasoning for the stage, or its default (DefaultReasoning). A stage with no model uses the client's
// default model.
func (o CallOptions) ModelLLM(llm LLM, stage string) LLM {
	return &modelLLM{LLM: llm, stage: stage, model: o.Models[stage], reasoning: o.Reasoning.Level(stage)}
}

type modelLLM struct {
	LLM
	stage, model, reasoning string
}

func (m *modelLLM) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if req.Model == "" {
		req.Model = m.model
	}
	if req.Reasoning == "" {
		req.Reasoning = m.reasoning
	}
	return m.LLM.Complete(WithStage(ctx, m.stage), req)
}

type stageKey struct{}

// WithStage returns ctx that carries the pipeline stage a model call is made for. WithRecording stores it in the model
// run of the call.
func WithStage(ctx context.Context, stage string) context.Context {
	return context.WithValue(ctx, stageKey{}, stage)
}

// StageOf returns the stage of ctx (see WithStage), "" if there is none.
func StageOf(ctx context.Context) string {
	s, _ := ctx.Value(stageKey{}).(string)
	return s
}

//---------------------------------------------------------------------------------------------

// StageQuery is the LLM call of Query that answers the question. It is not a stage of remember (so it is not in Stages), but
// it takes a model (see QueryStages) and has a reasoning level of its own.
const StageQuery = "query"

// ReasoningStages lists the stages that take a reasoning level: the LLM stages of remember (see Stages) but the web
// search, which runs on Anthropic's web search tool without thinking (and Jev, which does not think either).
var ReasoningStages = []string{StageExtractGraph, StageSummarizeChunk, StageResolveEdges, StageResolveSeries, StageDetectGeo, StageExtractImage}

// The reasoning level a stage runs with unless the call chooses another. It is none (no thinking) for both pipelines, so
// that no thinking tokens are paid for by default; a call turns it on per stage (WithReasoning).
const (
	DefaultRememberReasoning = ai.ReasoningNone
	DefaultQueryReasoning    = ai.ReasoningNone
)

// DefaultReasoning is the reasoning level of a stage when the call sets none: DefaultQueryReasoning for StageQuery,
// DefaultRememberReasoning for a stage of ReasoningStages and none for any other.
func DefaultReasoning(stage string) string {
	if stage == StageQuery {
		return DefaultQueryReasoning
	}
	for _, s := range ReasoningStages {
		if s == stage {
			return DefaultRememberReasoning
		}
	}
	return ai.ReasoningNone
}

// ErrInvalidReasoning means a stage that takes no reasoning level is named, or a level is unknown.
var ErrInvalidReasoning = errors.New("invalid reasoning")

// Reasoning maps a stage (see ReasoningStages, and StageQuery for Query) to its reasoning level (ai.ReasoningLow, ...).
// A stage that is missing runs with its default (DefaultReasoning, none: no thinking); "low", "medium" and "high" turn it on.
type Reasoning map[string]string

// ResolveReasoning validates m, the reasoning levels of the stages of remember, and returns a normalized copy: levels are
// trimmed and lowercased, and a stage with an empty level is dropped (it means the default). "none" is kept, so a
// request can be explicit about it. It returns ErrInvalidReasoning for a stage that takes no reasoning level or an unknown level. A nil or
// empty m gives nil.
func ResolveReasoning(m map[string]string) (Reasoning, error) {
	return resolveReasoning(m, ReasoningStages)
}

// QueryReasoningStages lists the stages of Query that take a reasoning level: the one that answers (StageQuery) and the
// detection of the places in the question (StageDetectGeo, only with a geo resolver). They are the QueryStages.
var QueryReasoningStages = []string{StageDetectGeo, StageQuery}

// ResolveQueryReasoningStages is ResolveReasoning for Query: m maps the stages of QueryReasoningStages to their levels. A stage of
// remember that Query does not run is rejected.
func ResolveQueryReasoningStages(m map[string]string) (Reasoning, error) {
	return resolveReasoning(m, QueryReasoningStages)
}

func resolveReasoning(m map[string]string, stages []string) (Reasoning, error) {
	var out Reasoning
	for stage, level := range m {
		if !slices.Contains(stages, stage) {
			return nil, fmt.Errorf("%w: unknown stage %q (stages with a reasoning level: %s)", ErrInvalidReasoning, stage, strings.Join(stages, ", "))
		}
		l, err := resolveLevel(stage, level)
		if err != nil {
			return nil, err
		}
		if l == "" {
			continue
		}
		if out == nil {
			out = Reasoning{}
		}
		out[stage] = l
	}
	return out, nil
}

// ResolveQueryReasoning validates the reasoning level of the answering call of Query (StageQuery) and returns it as a Reasoning for
// WithReasoning. An empty level means the default (DefaultQueryReasoning) and gives nil. ResolveQueryReasoningStages takes a
// level per stage.
func ResolveQueryReasoning(level string) (Reasoning, error) {
	l, err := resolveLevel(StageQuery, level)
	if err != nil || l == "" {
		return nil, err
	}
	return Reasoning{StageQuery: l}, nil
}

// resolveLevel normalizes a level; "" is returned for an empty one (the default).
func resolveLevel(stage, level string) (string, error) {
	if strings.TrimSpace(level) == "" {
		return "", nil
	}
	l, valid := ai.NormalizeReasoning(level)
	if !valid {
		return "", fmt.Errorf("%w: %s: unknown level %q (levels: %s)", ErrInvalidReasoning, stage, level, strings.Join(ai.ReasoningLevels, ", "))
	}
	return l, nil
}

// WithReasoning sets the reasoning level of stages (see ReasoningStages; StageQuery for Query). A stage that is not in r
// runs with its default level (DefaultReasoning). Validate r with ResolveReasoning (or ResolveQueryReasoning) first.
func WithReasoning(r Reasoning) Option { return func(o *CallOptions) { o.Reasoning = r } }

// Level is the reasoning level a stage runs with, as it is recorded: the one set for it, or its default
// (DefaultReasoning).
func (r Reasoning) Level(stage string) string {
	if l := r[stage]; l != "" {
		return l
	}
	return DefaultReasoning(stage)
}
