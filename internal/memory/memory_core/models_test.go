package memory_core

import (
	"context"
	"errors"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
)

func TestResolveModels(t *testing.T) {
	m, err := ResolveModels(map[string]string{"extract_graph": " a ", "resolve_edges": "  ", "extract_image": "c"})
	if err != nil || len(m) != 2 || m["extract_graph"] != "a" || m["extract_image"] != "c" {
		t.Fatalf("got %v, %v", m, err)
	}
	if m, err := ResolveModels(nil); m != nil || err != nil {
		t.Errorf("nil: %v, %v", m, err)
	}
	for _, bad := range []map[string]string{{"nope": "a"}, {"extract_graph": "a b"}, {"extract_graph": "a\x00b"}} {
		if _, err := ResolveModels(bad); !errors.Is(err, ErrInvalidModels) {
			t.Errorf("%v: err = %v", bad, err)
		}
	}
}

func TestParseAndMergeModels(t *testing.T) {
	m, err := ParseModels("extract_graph=a, resolve_edges=b")
	if err != nil || m.String() != "extract_graph=a,resolve_edges=b" {
		t.Fatalf("got %v, %v", m, err)
	}
	if _, err := ParseModels("extract_graph"); err == nil {
		t.Error("missing = accepted")
	}
	got := m.Merge(Models{"resolve_edges": "c", "extract_image": "d"})
	if got.String() != "extract_graph=a,resolve_edges=c,extract_image=d" || m["resolve_edges"] != "b" {
		t.Errorf("merge = %v (base %v)", got, m)
	}
	if Models(nil).Merge(nil) != nil {
		t.Error("merge of nothing is not nil")
	}
}

type captureLLM struct{ model string }

func (c *captureLLM) Complete(_ context.Context, r ai.Request) (*ai.Response, error) {
	c.model = r.Model
	return &ai.Response{}, nil
}

func TestModelLLM(t *testing.T) {
	o := NewCallOptions([]Option{WithModels(Models{StageExtractGraph: "x"})})
	c := &captureLLM{}
	o.ModelLLM(c, StageExtractGraph).Complete(context.Background(), ai.Request{})
	if c.model != "x" {
		t.Errorf("extract model = %q", c.model)
	}
	o.ModelLLM(c, StageResolveEdges).Complete(context.Background(), ai.Request{})
	if c.model != "" {
		t.Errorf("resolve (unset) model = %q, want the default", c.model)
	}
	o.ModelLLM(c, StageExtractGraph).Complete(context.Background(), ai.Request{Model: "explicit"})
	if c.model != "explicit" {
		t.Errorf("an explicit request model was overridden: %q", c.model)
	}
}

func TestReasoningDefaults(t *testing.T) {
	var r Reasoning
	for _, s := range ReasoningStages {
		if r.Level(s) != ai.ReasoningNone {
			t.Errorf("%s default = %q, want none", s, r.Level(s))
		}
	}
	if r.Level(StageRecall) != ai.ReasoningNone {
		t.Errorf("recall default = %q, want none", r.Level(StageRecall))
	}
	if r.Level(StageWebSearch) != ai.ReasoningNone {
		t.Errorf("web_search default = %q, want none", r.Level(StageWebSearch))
	}

	// "none" is kept (it turns thinking off); an empty level is the default.
	got, err := ResolveReasoning(map[string]string{"summarize_chunk": " None ", "extract_graph": "", "resolve_edges": "HIGH"})
	if err != nil || len(got) != 2 || got.Level(StageSummarizeChunk) != "none" || got.Level(StageResolveEdges) != "high" || got.Level(StageExtractGraph) != "none" {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []map[string]string{{"web_search": "low"}, {"recall": "low"}, {"extract_graph": "max"}} {
		if _, err := ResolveReasoning(bad); !errors.Is(err, ErrInvalidReasoning) {
			t.Errorf("%v: err = %v", bad, err)
		}
	}
	if r, err := ResolveRecallReasoning(" High"); err != nil || r.Level(StageRecall) != "high" {
		t.Errorf("recall: %v, %v", r, err)
	}
	if r, err := ResolveRecallReasoning(""); err != nil || r != nil {
		t.Errorf("recall default: %v, %v", r, err)
	}
	if _, err := ResolveRecallReasoning("max"); !errors.Is(err, ErrInvalidReasoning) {
		t.Errorf("recall: err = %v", err)
	}
}

// ModelLLM applies the reasoning level of the stage (its default, none, if none was set) and tells the stage to the recorder.
func TestModelLLMReasoningAndStage(t *testing.T) {
	var gotReq ai.Request
	var gotStage string
	llm := llmFunc(func(ctx context.Context, r ai.Request) (*ai.Response, error) {
		gotReq, gotStage = r, StageOf(ctx)
		return &ai.Response{}, nil
	})
	o := NewCallOptions([]Option{WithReasoning(Reasoning{StageSummarizeChunk: "low", StageRecall: "high"})})
	for _, tc := range []struct{ stage, want string }{
		{StageExtractGraph, "none"}, {StageSummarizeChunk, "low"}, {StageRecall, "high"},
	} {
		o.ModelLLM(llm, tc.stage).Complete(context.Background(), ai.Request{})
		if gotReq.Reasoning != tc.want || gotStage != tc.stage {
			t.Errorf("%s: reasoning %q, stage %q; want %q", tc.stage, gotReq.Reasoning, gotStage, tc.want)
		}
	}
	NewCallOptions(nil).ModelLLM(llm, StageRecall).Complete(context.Background(), ai.Request{})
	if gotReq.Reasoning != "none" {
		t.Errorf("recall default = %q", gotReq.Reasoning)
	}
	NewCallOptions(nil).ModelLLM(llm, StageExtractGraph).Complete(context.Background(), ai.Request{Reasoning: "high"})
	if gotReq.Reasoning != "high" {
		t.Errorf("an explicit request level was overridden: %q", gotReq.Reasoning)
	}
}

type llmFunc func(ctx context.Context, r ai.Request) (*ai.Response, error)

func (f llmFunc) Complete(ctx context.Context, r ai.Request) (*ai.Response, error) { return f(ctx, r) }
