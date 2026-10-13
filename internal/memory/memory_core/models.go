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
	// StageResolveEdges is the comparison of the new edges with the stored ones (duplicates, contradictions).
	StageResolveEdges = "resolve_edges"
	// StageExtractImage is the reading of an image (the image pipeline).
	StageExtractImage = "extract_image"
)

// Stages lists the stages that take a model, in pipeline order.
var Stages = []string{StageExtractGraph, StageResolveEdges, StageExtractImage}

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

// WithModels sets the model of each LLM stage of Remember and RememberImage (see the Stage constants). A stage that is
// not in m, or whose model is empty, uses the default model. Validate m with ResolveModels first.
func WithModels(m Models) Option { return func(o *CallOptions) { o.Models = m } }

// ModelLLM returns llm with the model of the given stage set on every request that names none. With no model for the
// stage it returns llm itself, so the client's default model is used.
func (o CallOptions) ModelLLM(llm LLM, stage string) LLM {
	model := o.Models[stage]
	if model == "" {
		return llm
	}
	return &modelLLM{LLM: llm, model: model}
}

type modelLLM struct {
	LLM
	model string
}

func (m *modelLLM) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if req.Model == "" {
		req.Model = m.model
	}
	return m.LLM.Complete(ctx, req)
}
