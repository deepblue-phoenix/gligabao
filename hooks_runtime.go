package gligabao

import (
	"context"

	"github.com/deepblue-phoenix/gligabao/gbruntime"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/training"
)

// Every hook of the embedding program is called with the Runtime: the clients gligabao assembled when the server started (the
// databases, ...), so the hook reuses them instead of opening its own. The hooks of this file are the per-request ones; the
// lifecycle and event hooks are in package monitoring (Hooks.OnStart, Hooks.SystemEvent, Hooks.ProviderEvents) and take the Runtime too.
//
// The internal code of gligabao calls hooks without a Runtime; the functions below (bind*) close each public hook over the Runtime
// of the server (a gbruntime.Ref the server fills once the databases are open, before it accepts requests), and give the internal
// code the shape it knows. They are pure: they only build closures.

// Runtime is what gligabao has set up when the server starts, handed to every hook: see package gbruntime.
type Runtime = gbruntime.Runtime

// Authenticator is the auth hook (see ServeOptions.Auth and docs/hooks.md): it is called with the API key of a request and returns
// the ID of the user the key belongs to, or an error to reject it.
type Authenticator func(ctx context.Context,
	rt *Runtime,
	apiKey string) (userID string, err error)

// GraphEmbedder is the graph embedding hook (see ServeOptions.GraphEmbedder and docs/hooks.md): it returns the embedding of a whole graph.
//
// IMPORTANT: gligabao does not store the embeddings yet; they must be stored in the vector store.
type GraphEmbedder func(ctx context.Context,
	rt *Runtime,
	req GraphEmbedRequest) ([]float32, error)

// ClassifyingTrainingHook is the classifying training hook (see ServeOptions.ClassifyingTrainingHook and docs/hooks.md): it gets a whole
// knowledge dataset (every artifact and its graph) and returns the training dataset of a graph classifier, which may be a filtered subset.
type ClassifyingTrainingHook func(ctx context.Context,
	rt *Runtime,
	ds KnowledgeDataset) (TrainingDataset, error)

// TimeseriesFetchFunc is the Fetch callback of a TimeseriesProvider: it fetches the series for the request.
type TimeseriesFetchFunc func(ctx context.Context,
	rt *Runtime,
	req TimeseriesFetchRequest) ([]TimeseriesData, error)

// TimeseriesProvider is a source of timeseries that remember can fetch from (see ServeOptions.TimeseriesProviders and
// docs/timeseries.md). Description says in natural language when the provider applies, so an LLM can decide which
// entities it is for; Fetch is the callback, with the logic of fetching. Name, Description and QueryFormat are as in
// memory_core.SeriesProvider: Name is letters, digits, '_', '-' and '.', unique; QueryFormat says what Fetch needs to identify an
// entity (optional).
type TimeseriesProvider struct {
	Name        string
	Description string
	QueryFormat string
	Fetch       TimeseriesFetchFunc
}

// DeployTrainedModelFunc is the Deploy callback of a TrainedModelDeployer. It runs synchronously (the request that asked for the
// deployment waits for it), must honour ctx and may be called concurrently. A returned error (or a panic) marks the deployment failed.
type DeployTrainedModelFunc func(ctx context.Context,
	rt *Runtime,
	req DeployTrainedModelRequest) (DeployTrainedModelResult, error)

// TrainedModelDeployer is a named hook that deploys trained models (see ServeOptions.TrainedModelDeployers and
// docs/hooks.md): gligabao fetches the weights of the model to a local directory and calls Deploy with it; what Deploy does
// with the files (upload them to a third party, copy them, ...) is up to the embedding program. Name identifies it (letters, digits,
// '_', '-' and '.', unique); Description says in plain words what it does (optional).
type TrainedModelDeployer struct {
	Name        string
	Description string
	Deploy      DeployTrainedModelFunc
}

// InferTrainedModelFunc is the Infer callback of a TrainedModelInferenceHook: it runs one prompt. It must honour ctx and may be
// called concurrently. A returned error (or a panic) fails the request, and its text is what the user sees.
type InferTrainedModelFunc func(ctx context.Context,
	rt *Runtime,
	req InferTrainedModelRequest) (InferTrainedModelResult, error)

// TrainedModelInferenceHook is a named hook that runs the prompts of one trained model somewhere other than the ML sidecar (Modal): see
// ServeOptions.TrainedModelInferenceHooks and docs/hooks.md. Model is the name of the trained model it targets; when that model is
// prompted, gligabao calls Infer instead of the ML sidecar. Several hooks can be supplied, each for a different model. Name identifies
// the hook (unique); Description says where the prompts go (optional).
type TrainedModelInferenceHook struct {
	Name        string
	Description string
	Model       string
	Infer       InferTrainedModelFunc
}

// GeoResolver turns the name of a place into its coordinates (see ServeOptions.GeoResolver). Resolve returns the best place for the
// query, or GeoNotFound if it knows none; any other error means the resolver failed. It must be safe for concurrent use.
type GeoResolver interface {
	// Name identifies the resolver ("nominatim"); it is stored with what it resolved.
	Name() string
	Resolve(ctx context.Context,
		rt *Runtime,
		q GeoQuery) (*GeoPlace, error)
}

//---------------------------------------------------------------------------------------------

func bindAuth(ref *gbruntime.Ref,
	f Authenticator) httputil.Authenticator {
	if f == nil {
		return nil
	}
	return func(ctx context.Context,
		apiKey string) (string, error) {
		return f(ctx, ref.Get(), apiKey)
	}
}

func bindGraphEmbedder(ref *gbruntime.Ref,
	f GraphEmbedder) memory_core.GraphEmbedder {
	if f == nil {
		return nil
	}
	return func(ctx context.Context,
		req GraphEmbedRequest) ([]float32, error) {
		return f(ctx, ref.Get(), req)
	}
}

func bindClassifyingTrainingHook(ref *gbruntime.Ref,
	f ClassifyingTrainingHook) memory_core.ClassifyingTrainingHook {
	if f == nil {
		return nil
	}
	return func(ctx context.Context,
		ds KnowledgeDataset) (TrainingDataset, error) {
		return f(ctx, ref.Get(), ds)
	}
}

func bindTimeseriesProviders(ref *gbruntime.Ref,
	list []TimeseriesProvider) []memory_core.SeriesProvider {
	out := make([]memory_core.SeriesProvider, 0, len(list))
	for _, p := range list {
		bound := memory_core.SeriesProvider{
			Name:        p.Name,
			Description: p.Description,
			QueryFormat: p.QueryFormat,
		}
		if fetch := p.Fetch; fetch != nil { // a nil Fetch stays nil: ResolveSeriesProviders reports it
			bound.Fetch = func(ctx context.Context,
				req TimeseriesFetchRequest) ([]TimeseriesData, error) {
				return fetch(ctx, ref.Get(), req)
			}
		}
		out = append(out, bound)
	}
	return out
}

func bindDeployers(ref *gbruntime.Ref,
	list []TrainedModelDeployer) []training.TrainedModelDeployer {
	out := make([]training.TrainedModelDeployer, 0, len(list))
	for _, d := range list {
		bound := training.TrainedModelDeployer{
			Name:        d.Name,
			Description: d.Description,
		}
		if deploy := d.Deploy; deploy != nil {
			bound.Deploy = func(ctx context.Context,
				req DeployTrainedModelRequest) (DeployTrainedModelResult, error) {
				return deploy(ctx, ref.Get(), req)
			}
		}
		out = append(out, bound)
	}
	return out
}

func bindInferenceHooks(ref *gbruntime.Ref,
	list []TrainedModelInferenceHook) []training.TrainedModelInferenceHook {
	out := make([]training.TrainedModelInferenceHook, 0, len(list))
	for _, h := range list {
		bound := training.TrainedModelInferenceHook{
			Name:        h.Name,
			Description: h.Description,
			Model:       h.Model,
		}
		if infer := h.Infer; infer != nil {
			bound.Infer = func(ctx context.Context,
				req InferTrainedModelRequest) (InferTrainedModelResult, error) {
				return infer(ctx, ref.Get(), req)
			}
		}
		out = append(out, bound)
	}
	return out
}

// boundGeoResolver is a GeoResolver seen as a memory_geo.Resolver.
type boundGeoResolver struct {
	ref *gbruntime.Ref
	r   GeoResolver
}

func (b boundGeoResolver) Name() string { return b.r.Name() }

func (b boundGeoResolver) Resolve(ctx context.Context,
	q memory_geo.Query) (*memory_geo.Place, error) {
	return b.r.Resolve(ctx, b.ref.Get(), q)
}

// bindGeoResolver returns nil for a nil resolver (a nil interface, not a wrapper around one).
func bindGeoResolver(ref *gbruntime.Ref,
	r GeoResolver) memory_geo.Resolver {
	if r == nil {
		return nil
	}
	return boundGeoResolver{
		ref: ref,
		r:   r,
	}
}
