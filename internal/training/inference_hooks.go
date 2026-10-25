package training

// Inference hooks of trained models: code supplied by the program that embeds gligabao (ServeOptions.TrainedModelInferenceHooks)
// that runs the prompts of a trained model instead of the sidecar (Modal). Each hook names the trained model it targets; when a
// model is prompted, gligabao looks for the hook that targets it and, if there is one, calls it and does not touch the sidecar.
// A model no hook targets is run on the sidecar as before.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

// ErrInvalidInferenceHook means a registered inference hook is malformed.
var ErrInvalidInferenceHook = errors.New("invalid trained model inference hook")

// The limits of inference hooks.
const (
	MaxInferenceHookNameLen  = 64
	MaxInferenceHookModelLen = 256
	MaxInferenceHooks        = 50
)

var hookNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// InferTrainedModelRequest is a prompt a hook is asked to run.
type InferTrainedModelRequest struct {
	// Model is the trained model that is prompted (the one the hook targets).
	Model trainingdb.TrainedModel
	// Prompt is the text to run.
	Prompt string
	// MaxTokens is the limit of the answer's tokens; 0 means the caller chose none: the hook uses its own default.
	MaxTokens int
	// Temperature is the sampling temperature; nil means the caller chose none: the hook uses its own default.
	Temperature *float64
}

// InferTrainedModelResult is what a hook answers.
type InferTrainedModelResult struct {
	// Response is the model's reply.
	Response string
	// InputTokens and OutputTokens are the token counts the model reported (0 if unknown); they are stored with the request.
	InputTokens  int
	OutputTokens int
}

// InferTrainedModelFunc runs one prompt. It must honour ctx (the caller's limit, InferTimeout) and may be called concurrently. A
// returned error (or a panic) fails the request, and its text is what the user sees.
type InferTrainedModelFunc func(ctx context.Context, req InferTrainedModelRequest) (InferTrainedModelResult, error)

// TrainedModelInferenceHook is a named way of running the prompts of one trained model somewhere other than the sidecar.
type TrainedModelInferenceHook struct {
	// Name identifies the hook: letters, digits, '_', '-' and '.', starting with a letter or digit, unique. It is logged, and
	// returned with the answer of the infer endpoint.
	Name string
	// Description says in plain words where the prompts go (optional).
	Description string
	// Model is the name of the trained model the hook targets (gb_trained_models.name, as the models are listed in the admin UI,
	// e.g. "4b-mine.jsonl-100steps-1a2b3c4d"). The hook runs the prompts of every trained model with exactly that name (surrounding
	// whitespace ignored, case-sensitive). At most one hook may target a name.
	Model string
	// Infer runs the prompt.
	Infer InferTrainedModelFunc
}

// ResolveInferenceHooks validates the hooks registered at startup (ErrInvalidInferenceHook, with what is wrong, if one is malformed) and
// returns them, with Model trimmed, sorted by name.
func ResolveInferenceHooks(list []TrainedModelInferenceHook) ([]TrainedModelInferenceHook, error) {
	if len(list) > MaxInferenceHooks {
		return nil, fmt.Errorf("%w: at most %d may be registered, got %d", ErrInvalidInferenceHook, MaxInferenceHooks, len(list))
	}
	names, models := map[string]bool{}, map[string]string{}
	out := make([]TrainedModelInferenceHook, 0, len(list))
	for i, h := range list {
		h.Model = strings.TrimSpace(h.Model)
		switch {
		case h.Name == "" || len(h.Name) > MaxInferenceHookNameLen || !hookNameRE.MatchString(h.Name):
			return nil, fmt.Errorf("%w: hook %d: name %q must be 1-%d letters, digits, '_', '-' or '.', starting with a letter or digit", ErrInvalidInferenceHook, i, h.Name, MaxInferenceHookNameLen)
		case names[h.Name]:
			return nil, fmt.Errorf("%w: duplicate hook name %q", ErrInvalidInferenceHook, h.Name)
		case h.Model == "":
			return nil, fmt.Errorf("%w: hook %q has no Model: it must name the trained model it targets", ErrInvalidInferenceHook, h.Name)
		case len(h.Model) > MaxInferenceHookModelLen || strings.ContainsFunc(h.Model, func(r rune) bool { return r < 0x20 || r == 0x7f }):
			return nil, fmt.Errorf("%w: hook %q: model %q must be at most %d bytes, without control characters", ErrInvalidInferenceHook, h.Name, h.Model, MaxInferenceHookModelLen)
		case models[h.Model] != "":
			return nil, fmt.Errorf("%w: hooks %q and %q target the same model %q", ErrInvalidInferenceHook, models[h.Model], h.Name, h.Model)
		case h.Infer == nil:
			return nil, fmt.Errorf("%w: hook %q has no Infer function", ErrInvalidInferenceHook, h.Name)
		}
		names[h.Name], models[h.Model] = true, h.Name
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// InferRouter decides where the prompts of a trained model run: on the inference hook that targets the model's name, else on the
// sidecar. The zero value has neither. Build it with NewInferRouter; it is safe for concurrent use.
type InferRouter struct {
	sidecar Inferer // nil: no sidecar
	byModel map[string]TrainedModelInferenceHook
}

// NewInferRouter makes the router of sc (nil: no sidecar, so only the models that have a hook can run) and hooks (validated with
// ResolveInferenceHooks).
func NewInferRouter(sc Inferer, hooks []TrainedModelInferenceHook) *InferRouter {
	r := &InferRouter{sidecar: sc, byModel: make(map[string]TrainedModelInferenceHook, len(hooks))}
	for _, h := range hooks {
		r.byModel[strings.TrimSpace(h.Model)] = h
	}
	return r
}

// HookFor returns the hook that targets trained model tm (matched by its name), if there is one.
func (r *InferRouter) HookFor(tm *trainingdb.TrainedModel) (TrainedModelInferenceHook, bool) {
	if r == nil {
		return TrainedModelInferenceHook{}, false
	}
	h, ok := r.byModel[strings.TrimSpace(tm.Name)]
	return h, ok
}

// For returns what runs the prompts of trained model tm: its hook if one targets it (the sidecar is then bypassed), else the
// sidecar. It is nil when neither is available (no hook and no sidecar). hook is the name of the hook, "" for the sidecar.
func (r *InferRouter) For(tm *trainingdb.TrainedModel) (inf Inferer, hook string) {
	if h, ok := r.HookFor(tm); ok {
		return hookInferer{hook: h, model: *tm}, h.Name
	}
	if r == nil || r.sidecar == nil {
		return nil, ""
	}
	return r.sidecar, ""
}

// hookInferer adapts the hook of a trained model to the sidecar's interface, so that the infer endpoint treats a hook and the sidecar the same.
type hookInferer struct {
	hook  TrainedModelInferenceHook
	model trainingdb.TrainedModel
}

// Infer calls the hook. A failure is an *sidecar.InferError, whose message is shown to the user as the reason.
func (h hookInferer) Infer(ctx context.Context, req sidecar.InferRequest) (out sidecar.InferResponse, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &sidecar.InferError{Message: fmt.Sprintf("inference hook %q panicked: %v", h.hook.Name, r)}
		}
	}()
	res, err := h.hook.Infer(ctx, InferTrainedModelRequest{Model: h.model, Prompt: req.Prompt, MaxTokens: req.MaxTokens, Temperature: req.Temperature})
	if err != nil {
		return out, &sidecar.InferError{Message: fmt.Sprintf("inference hook %q failed: %v", h.hook.Name, err)}
	}
	if res.InputTokens < 0 || res.OutputTokens < 0 {
		return out, &sidecar.InferError{Message: fmt.Sprintf("inference hook %q reported negative token counts", h.hook.Name)}
	}
	return sidecar.InferResponse{Response: res.Response, ModelSize: h.model.ModelSize, ModelPath: h.model.Path,
		InputTokens: res.InputTokens, OutputTokens: res.OutputTokens}, nil
}

// Hooks are the hooks the embedding program registered for the training API (already validated: see ResolveExporters and
// ResolveInferenceHooks); the zero value has none.
type Hooks struct {
	// Exporters export trained models (POST /v1/gb/training/models/{id}/exports).
	Exporters []TrainedModelExporter
	// Inference run the prompts of the trained models they target, bypassing the sidecar.
	Inference []TrainedModelInferenceHook
}
