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
