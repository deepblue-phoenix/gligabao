package memory_core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"

	log "github.com/sirupsen/logrus"
)

// TrajectoryDir is where Remember, Recall and RememberImage store their ATIF trajectories (one pretty-printed JSON
// file per call, named <trajectory_id>.json). It is read on every call; set it once at startup, or override it per
// call with WithTrajectoryDir.
var TrajectoryDir = "bin/atif"

const agentVersion = "0.1"

//---------------------------------------------------------------------------------------------

// Embedder turns texts into embeddings (see ai.Embedder), one per text and in the same order, and reports the usage.
type Embedder interface {
	EmbedBatchUsage(ctx context.Context, texts []string) ([][]float32, *ai.EmbedUsage, error)
}

// Option customizes a single Remember, Recall or RememberImage call. Without WithEval no trajectory is generated.
type Option func(*CallOptions)

// CallOptions are the settings of one call, built from Options by NewCallOptions. Each pipeline reads the fields
// it uses and ignores the others.
type CallOptions struct {
	Eval bool
	Dir  string // the trajectory directory

	ImageDir string // the image directory; "" means the default of package memory_image

	Vectors  vector.Store // the vector store of the text pipeline
	Embedder Embedder

	Trace *Trace // set by Remember for the Store it calls; nil otherwise

	// Chunking says when and how Remember splits a long text into chunks (see package chunk): a text of more than
	// Chunking.Size tokens is. The default is chunk.DefaultConfig().
	Chunking chunk.Config

	// SessionID is the user/agent session the call belongs to ("" for none), set by WithSessionID. Remember and
	// RememberImage attach it to what they store; Recall only records it (it never changes the answer).
	SessionID string
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
// yields (see gb_entity_sessions, gb_edge_sessions), RememberImage to the image artifact; Recall only carries it
// into its result and trajectory, and its answer does not depend on it. The ID is used as given: validate it with
// ResolveSessionID first. Empty means no session.
func WithSessionID(id string) Option { return func(o *CallOptions) { o.SessionID = id } }

// WithChunking sets the chunk size (in tokens) and overlap that Remember uses for a text larger than one chunk. See
// chunk.Config; an invalid one makes Remember fail. The default is chunk.DefaultConfig().
func WithChunking(c chunk.Config) Option { return func(o *CallOptions) { o.Chunking = c } }

// WithTrace makes Store record its steps (the embedding) into tr, the trace of the enclosing Remember.
func WithTrace(tr *Trace) Option { return func(o *CallOptions) { o.Trace = tr } }

// WithTrajectoryDir overrides TrajectoryDir for this call.
func WithTrajectoryDir(dir string) Option { return func(o *CallOptions) { o.Dir = dir } }

//---------------------------------------------------------------------------------------------

// Trace records one Remember/Recall/RememberImage call as an ATIF trajectory. A nil *Trace (eval not requested) is
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
