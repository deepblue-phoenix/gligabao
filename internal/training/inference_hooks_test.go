package training_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

func hook(name, model string) core.TrainedModelInferenceHook {
	return core.TrainedModelInferenceHook{Name: name, Model: model, Infer: func(_ context.Context, req core.InferTrainedModelRequest) (core.InferTrainedModelResult, error) {
		return core.InferTrainedModelResult{Response: name + ":" + req.Prompt, InputTokens: 3, OutputTokens: 4}, nil
	}}
}

type countingSidecar struct{ calls int }

func (c *countingSidecar) Infer(context.Context, sidecar.InferRequest) (sidecar.InferResponse, error) {
	c.calls++
	return sidecar.InferResponse{Response: "sidecar"}, nil
}

func TestResolveInferenceHooks(t *testing.T) {
	got, err := core.ResolveInferenceHooks([]core.TrainedModelInferenceHook{hook("b", " 4b-x "), hook("a", "4b-y")})
	if err != nil || len(got) != 2 || got[0].Name != "a" || got[1].Model != "4b-x" {
		t.Fatalf("got %+v, %v", got, err)
	}
	for name, list := range map[string][]core.TrainedModelInferenceHook{
		"bad name":       {hook("a b", "m")},
		"no model":       {hook("a", "  ")},
		"control char":   {hook("a", "m\nx")},
		"duplicate name": {hook("a", "m1"), hook("a", "m2")},
		"same model":     {hook("a", "m"), hook("b", "m")},
		"no infer":       {{Name: "a", Model: "m"}},
	} {
		if _, err := core.ResolveInferenceHooks(list); !errors.Is(err, core.ErrInvalidInferenceHook) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestInferRouter(t *testing.T) {
	sc := &countingSidecar{}
	hooks, _ := core.ResolveInferenceHooks([]core.TrainedModelInferenceHook{hook("h1", "model-1"), hook("h2", "model-2")})
	r := core.NewInferRouter(sc, hooks)
	ctx := context.Background()
	m1, m2, m3 := &trainingdb.TrainedModel{Name: "model-1", Path: "trained/1", ModelSize: "4b"}, &trainingdb.TrainedModel{Name: "model-2"}, &trainingdb.TrainedModel{Name: "other"}

	inf, name := r.For(m1)
	out, err := inf.Infer(ctx, sidecar.InferRequest{Prompt: "p"})
	if err != nil || name != "h1" || out.Response != "h1:p" || out.InputTokens != 3 || out.OutputTokens != 4 || out.ModelPath != "trained/1" {
		t.Fatalf("m1: %q %+v %v", name, out, err)
	}
	if inf, name = r.For(m2); name != "h2" {
		t.Fatalf("m2: %q", name)
	} else if out, _ = inf.Infer(ctx, sidecar.InferRequest{Prompt: "q"}); out.Response != "h2:q" {
		t.Fatalf("m2: %+v", out)
	}
	if sc.calls != 0 {
		t.Fatalf("the sidecar was called for hooked models")
	}
	if inf, name = r.For(m3); inf != core.Inferer(sc) || name != "" {
		t.Fatalf("m3: %v %q", inf, name)
	}

	// without a sidecar only hooked models run
	noSC := core.NewInferRouter(nil, hooks)
	if inf, _ := noSC.For(m1); inf == nil {
		t.Error("hooked model without a sidecar: want the hook")
	}
	if inf, _ := noSC.For(m3); inf != nil {
		t.Error("model without hook or sidecar: want nil")
	}
	if inf, _ := (*core.InferRouter)(nil).For(m3); inf != nil {
		t.Error("nil router: want nil")
	}
}

func TestInferRouterHookFailures(t *testing.T) {
	fail := core.TrainedModelInferenceHook{Name: "f", Model: "mf", Infer: func(context.Context, core.InferTrainedModelRequest) (core.InferTrainedModelResult, error) {
		return core.InferTrainedModelResult{}, errors.New("boom")
	}}
	pan := core.TrainedModelInferenceHook{Name: "p", Model: "mp", Infer: func(context.Context, core.InferTrainedModelRequest) (core.InferTrainedModelResult, error) {
		panic("oops")
	}}
	neg := core.TrainedModelInferenceHook{Name: "n", Model: "mn", Infer: func(context.Context, core.InferTrainedModelRequest) (core.InferTrainedModelResult, error) {
		return core.InferTrainedModelResult{OutputTokens: -1}, nil
	}}
	r := core.NewInferRouter(nil, []core.TrainedModelInferenceHook{fail, pan, neg})
	for model, want := range map[string]string{"mf": "boom", "mp": "panicked: oops", "mn": "negative"} {
		inf, _ := r.For(&trainingdb.TrainedModel{Name: model})
		_, err := inf.Infer(context.Background(), sidecar.InferRequest{Prompt: "p"})
		var ie *sidecar.InferError
		if !errors.As(err, &ie) || !strings.Contains(ie.Message, want) {
			t.Errorf("%s: err = %v", model, err)
		}
	}
}
