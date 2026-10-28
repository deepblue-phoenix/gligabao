package memory_core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
	"github.com/deepblue-phoenix/gligabao/internal/sys/jev"

	log "github.com/sirupsen/logrus"
)

// TrajectoryDir is where Remember, Query and RememberImage store their ATIF trajectories (one pretty-printed JSON
// file per call, named <trajectory_id>.json). It is read on every call; set it once at startup, or override it per
// call with WithTrajectoryDir.
var TrajectoryDir = "build/atif"

const agentVersion = "0.1"

//---------------------------------------------------------------------------------------------

// Embedder turns texts into embeddings (see ai.Embedder), one per text and in the same order, and reports the usage.
type Embedder interface {
	EmbedBatchUsage(ctx context.Context, texts []string) ([][]float32, *ai.EmbedUsage, error)
}

// Classifier is Jev (see package jev), the system 1 model, as far as the pipelines need it. *jev.Client implements it.
type Classifier interface {
	// Classify judges how many entities a text will yield. The jev.Call is valid even when the error is not nil.
	Classify(ctx context.Context, text string) (*jev.Classification, jev.Call, error)
}

// Option customizes a single Remember, Query or RememberImage call. Without WithEval no trajectory is generated.
type Option func(*CallOptions)

// CallOptions are the settings of one call, built from Options by NewCallOptions. Each pipeline reads the fields
// it uses and ignores the others.
type CallOptions struct {
	Eval bool
	Dir  string // the trajectory directory

	ImageDir string // the image directory; "" means the default of package memory_image

	Vectors  vector.Store // the vector store of the text pipeline
	Embedder Embedder

	// Jev is the system 1 model, set by WithJev; nil (the default) disables every step that needs it.
	Jev Classifier

	// WebSearcher is what runs web searches, set by WithWebSearcher; nil (the default) means web search is not
	// available (no Anthropic API key). WebSearch is the per-call request for it, set by WithWebSearch.
	WebSearcher WebSearcher
	WebSearch   bool

	Trace *Trace // set by Remember for the Store it calls; nil otherwise

	// Chunking says when and how Remember splits a long text into chunks (see package chunk): a text of more than
	// Chunking.Size tokens is. The default is chunk.DefaultConfig().
	Chunking chunk.Config

	// SessionID is the user/agent session the call belongs to ("" for none), set by WithSessionID. Remember and
	// RememberImage attach it to what they store; Query only records it (it never changes the answer).
	SessionID string

	// Models is the model of each LLM stage of Remember and RememberImage, set by WithModels; a stage not in it uses the
	// default model. See ModelLLM.
	Models Models

	// Reasoning is the reasoning level of each stage that takes one (see ReasoningStages, StageQuery), set by WithReasoning;
	// a stage not in it runs with its default (see DefaultReasoning).
	Reasoning Reasoning

	// Series are the timeseries to anchor to what Remember stores, set by WithSeries (see docs/timeseries.md).
	Series []SeriesSpec

	// SeriesProviders are the registered timeseries providers, set by WithSeriesProviders; SeriesFetch is the per-call
	// request to fetch series from them, set by WithSeriesFetch (nil: nothing is fetched).
	SeriesProviders []SeriesProvider
	SeriesFetch     *SeriesFetch
	// SeriesObservations says Remember derives a series_observation artifact from each series it stores, set by
	// WithSeriesObservations.
	SeriesObservations bool

	// GeoResolver is what finds the coordinates of the entities that are places, set by WithGeoResolver; nil (the default)
	// means Remember does not look at places at all, and Query does not look for the places a question mentions.
	GeoResolver memory_geo.Resolver
	// GeoNearby turns the proximity search of Query on, set by WithGeoNearby; false (the default) leaves it off.
	GeoNearby bool
	// GeoRadiusKm is the radius of the proximity search of Query, set by WithGeoRadius; 0 means DefaultGeoRadiusKm.
	GeoRadiusKm float64

	// RememberRunID is the run log row (gb_remember_runs) Remember adopts instead of creating one, set by
	// WithRememberRunID; 0 means Remember creates its own. The remember manager sets it: it creates the row when a
	// request is queued, so that the caller has the ID right away.
	RememberRunID int64
}

// NewCallOptions applies opts on top of the defaults.
func NewCallOptions(opts []Option) CallOptions {
	o := CallOptions{Dir: TrajectoryDir, Chunking: chunk.DefaultConfig()}
	for _, f := range opts {
		f(&o)
	}
	return o
}

// WithEval makes the call generate an ATIF trajectory: it is stored in TrajectoryDir and returned, with its ID, in
// the result (TrajectoryID, Trajectory). Without it nothing is generated. It is meant for evals; only the
// eval HTTP endpoints set it.
func WithEval() Option { return func(o *CallOptions) { o.Eval = true } }

// WithSessionID attaches the call to a user/agent session, so that what comes from the same session can be
// connected. Remember and Store attach it to the artifact and record it against every entity and edge the artifact
// yields (see gb_entity_sessions, gb_edge_sessions), RememberImage to the image artifact; Query only carries it
// into its result and trajectory, and its answer does not depend on it. The ID is used as given: validate it with
// ResolveSessionID first. Empty means no session.
func WithSessionID(id string) Option { return func(o *CallOptions) { o.SessionID = id } }

// WithRememberRunID makes Remember log the call in the existing run log row with that ID (created when the request was
// queued, see memory_remember.Manager) instead of creating one. Without it (or with 0) Remember creates the row itself.
func WithRememberRunID(id int64) Option { return func(o *CallOptions) { o.RememberRunID = id } }

// WithSeries makes Remember (and Store) store the timeseries and anchor them: to the artifact the call creates, and to
// the entities and edges the anchors select once the text was stored. They are validated with ResolveSeries (which
// Remember also does, before any LLM call).
func WithSeries(s []SeriesSpec) Option { return func(o *CallOptions) { o.Series = s } }

// WithChunking sets the chunk size (in tokens) and overlap that Remember uses for a text larger than one chunk. See
// chunk.Config; an invalid one makes Remember fail. The default is chunk.DefaultConfig().
func WithChunking(c chunk.Config) Option { return func(o *CallOptions) { o.Chunking = c } }

// WithJev enables the steps of Remember that run on Jev (the complexity classification of the incoming text). A nil c
// (Jev not configured) disables them. Jev is not a per-stage model: it is not part of Models.
func WithJev(c Classifier) Option { return func(o *CallOptions) { o.Jev = c } }

// WithGeoResolver makes Remember find the places among the entities it extracts (an LLM decides which are a city, a
// country, a region or any other place) and ask r for their coordinates, which are stored in gb_entity_geo (see
// package memory_geo). A nil r (the default) leaves it off. Like the web search it runs after the text is stored, and its
// failure never fails the call. It is the one switch of geo: Query also finds the places a question mentions when it is set, asks r
// for the coordinates of those that are not entities of the dataset (and stores them), and gives the geo entities near them to the
// LLM that answers, but only when the call asks for it (see WithGeoNearby and WithGeoRadius).
func WithGeoResolver(r memory_geo.Resolver) Option { return func(o *CallOptions) { o.GeoResolver = r } }

// WithWebSearcher makes web search available to Remember: ws runs the searches of the calls that ask for them (see
// WithWebSearch). It does not turn web search on by itself. A nil ws (no Anthropic API key) leaves it unavailable.
func WithWebSearcher(ws WebSearcher) Option { return func(o *CallOptions) { o.WebSearcher = ws } }

// WithWebSearch sets whether this Remember call searches the web for its key entities (default false). Remember
// returns ErrWebSearchUnavailable if it is true and there is no searcher (WithWebSearcher).
func WithWebSearch(on bool) Option { return func(o *CallOptions) { o.WebSearch = on } }

// ErrWebSearchUnavailable means a call asked for a web search but none is configured: web search needs the Anthropic API
// key.
var ErrWebSearchUnavailable = errors.New("web search is not available: it needs an Anthropic API key (ANTHROPIC_API_KEY)")

// WithTrace makes Store record its steps (the embedding) into tr, the trace of the enclosing Remember.
func WithTrace(tr *Trace) Option { return func(o *CallOptions) { o.Trace = tr } }

// WithTrajectoryDir overrides TrajectoryDir for this call.
func WithTrajectoryDir(dir string) Option { return func(o *CallOptions) { o.Dir = dir } }

//---------------------------------------------------------------------------------------------

// Trace records one Remember/Query/RememberImage call as an ATIF trajectory. A nil *Trace (eval not requested) is
// valid and records nothing, so callers need no checks.
type Trace struct {
	b   *atif.Builder
	dir string
}

// StartTrace returns nil unless o asks for eval.
func StartTrace(kind string, o CallOptions) *Trace {
	if !o.Eval {
		return nil
	}
	t := &Trace{b: atif.NewBuilder(atif.Agent{Name: "gligabao-" + kind, Version: agentVersion}), dir: o.Dir}
	if o.SessionID != "" { // the trajectories of one session share the session ID
		t.b.SetSessionID(o.SessionID)
	}
	return t
}

// Wrap returns llm with every call recorded as steps of the trajectory.
func (t *Trace) Wrap(llm LLM) LLM {
	if t == nil {
		return llm
	}
	return &tracingLLM{LLM: llm, b: t.b}
}

func (t *Trace) User(message string) {
	if t != nil {
		t.b.User(message)
	}
}

func (t *Trace) Agent(message string) {
	if t != nil {
		t.b.Agent(atif.Step{Message: message})
	}
}

func (t *Trace) Input(v map[string]any) {
	if t != nil {
		t.b.SetExtra("input", v)
	}
}

// Finish stores the trajectory and returns it (nil if there is no trace). A failed call is stored too (with the
// error in the notes), but the caller only sees the trajectory of a successful one. Failing to store the
// trajectory never fails the call.
func (t *Trace) Finish(callErr error) *atif.Trajectory {
	if t == nil {
		return nil
	}
	if callErr != nil {
		t.b.SetNotes("failed: " + callErr.Error())
	}
	traj := t.b.Build()
	if path, err := atif.WriteFile(t.dir, traj); err != nil {
		log.WithError(err).WithField("trajectory_id", traj.TrajectoryID).Error("store ATIF trajectory")
	} else {
		log.WithFields(log.Fields{"trajectory_id": traj.TrajectoryID, "path": path}).Debug("ATIF trajectory stored")
	}
	return traj
}

// Tool records a tool step whose arguments and result are summarized from Go values.
func (t *Trace) Tool(message, name string, args map[string]any, result any) {
	if t == nil {
		return
	}
	res, ok := result.(string)
	if !ok {
		b, _ := json.Marshal(result)
		res = string(b)
	}
	t.b.Tool(message, name, args, res)
}

// EmbedStep records an embedding request as a tool step with its tokens and cost, so they count in the
// trajectory's totals. res gets the usage added.
func (t *Trace) EmbedStep(message, name string, args, res map[string]any, usage *ai.EmbedUsage) {
	if t == nil {
		return
	}
	step := atif.Step{Message: message}
	if usage != nil {
		step.ModelName = usage.Model
		step.Metrics = &atif.Metrics{PromptTokens: usage.InputTokens, CostUSD: usage.CostUSD}
		step.Extra = map[string]any{"provider": usage.Provider}
		res["input_tokens"] = usage.InputTokens
	}
	b, _ := json.Marshal(res)
	t.b.ToolStep(step, name, args, string(b))
}

// ToolTiming is what TimedTool records about a step besides its arguments and result: which model ran it, when it
// started and how long it took, and its tokens (0 if unknown; the cost is not known for models outside the price table).
type ToolTiming struct {
	Model, Provider  string
	Started          time.Time
	Latency          time.Duration
	PromptTokens     int
	CompletionTokens int
	CostUSD          float64
}

// TimedTool is Tool for a step that took measurable time (a call to a model other than the LLM client's): the step's
// timestamp is the start of the call, and its extra holds latency_ms, started_at and the provider, so latency can be
// compared across runs.
func (t *Trace) TimedTool(message, name string, args map[string]any, result any, tm ToolTiming) {
	if t == nil {
		return
	}
	res, ok := result.(string)
	if !ok {
		b, _ := json.Marshal(result)
		res = string(b)
	}
	step := atif.Step{Message: message, ModelName: tm.Model, Extra: map[string]any{
		"provider":   tm.Provider,
		"latency_ms": float64(tm.Latency.Microseconds()) / 1000,
	}}
	if !tm.Started.IsZero() {
		step.Timestamp = tm.Started.UTC().Format(time.RFC3339Nano)
		step.Extra["started_at"] = step.Timestamp
	}
	if tm.PromptTokens > 0 || tm.CompletionTokens > 0 || tm.CostUSD > 0 {
		step.Metrics = &atif.Metrics{PromptTokens: tm.PromptTokens, CompletionTokens: tm.CompletionTokens, CostUSD: tm.CostUSD}
	}
	t.b.ToolStep(step, name, args, res)
}

//---------------------------------------------------------------------------------------------

// describeImages is how the images of a message appear in a trajectory: their bytes are not kept.
func describeImages(images []ai.Image) string {
	var sb strings.Builder
	for _, im := range images {
		fmt.Fprintf(&sb, "[image: %s, %d bytes]\n", im.MediaType, len(im.Data))
	}
	return sb.String()
}

// tracingLLM records each LLM call: the system prompt and input messages, then the model's output with its metrics.
type tracingLLM struct {
	LLM
	b *atif.Builder
}

func (l *tracingLLM) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if req.System != "" {
		l.b.System(req.System)
	}
	for _, m := range req.Messages {
		if m.Role == ai.RoleAssistant {
			l.b.Agent(atif.Step{Message: m.Content})
			continue
		}
		l.b.User(describeImages(m.Images) + m.Content)
	}
	resp, err := l.LLM.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	l.b.Agent(atif.Step{
		ModelName: resp.Model,
		Message:   resp.Text,
		Metrics:   &atif.Metrics{PromptTokens: resp.InputTokens, CompletionTokens: resp.OutputTokens, CostUSD: resp.CostUSD},
		Extra:     map[string]any{"provider": resp.Provider, "stop_reason": resp.StopReason},
	})
	return resp, nil
}
