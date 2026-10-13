package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"

	log "github.com/sirupsen/logrus"
)

// TrajectoryDir is where Remember and Recall store their ATIF trajectories (one pretty-printed JSON file per
// call, named <trajectory_id>.json). It is read on every call; set it once at startup, or override it per call
// with WithTrajectoryDir.
var TrajectoryDir = "bin/atif"

// ImageDir is where RememberImage stores the image files (see package imagestore); the database only tracks them.
// It is read on every call; set it once at startup, or override it per call with WithImageDir.
var ImageDir = "bin/images"

const agentVersion = "0.1"

//---------------------------------------------------------------------------------------------

// Option customizes a single Remember or Recall call. Without WithEval no trajectory is generated.
type Option func(*callOptions)

type callOptions struct {
	eval bool
	dir  string

	imageDir string // see WithImageDir

	vectors  vector.Store // see WithVectors
	embedder Embedder

	trace *trace // set by Remember for the Store it calls; nil otherwise
}

func newCallOptions(opts []Option) callOptions {
	o := callOptions{dir: TrajectoryDir, imageDir: ImageDir}
	for _, f := range opts {
		f(&o)
	}
	return o
}

// WithEval makes the call generate an ATIF trajectory: it is stored in TrajectoryDir and returned, with its ID, in
// Result / RecallResult (TrajectoryID, Trajectory). Without it nothing is generated. It is meant for evals; only the
// eval HTTP endpoints set it.
func WithEval() Option { return func(o *callOptions) { o.eval = true } }

// withTrace makes Store record its steps (the embedding) into tr, the trace of the enclosing Remember.
func withTrace(tr *trace) Option { return func(o *callOptions) { o.trace = tr } }

// WithTrajectoryDir overrides TrajectoryDir for this call.
func WithTrajectoryDir(dir string) Option { return func(o *callOptions) { o.dir = dir } }

// WithImageDir overrides ImageDir for this call: the directory RememberImage and LoadImage keep the image files in.
func WithImageDir(dir string) Option { return func(o *callOptions) { o.imageDir = dir } }

//---------------------------------------------------------------------------------------------

// trace records one Remember/Recall call as an ATIF trajectory. A nil *trace (eval not requested) is valid and
// records nothing, so callers need no checks.
type trace struct {
	b   *atif.Builder
	dir string
}

// startTrace returns nil unless o asks for eval.
func startTrace(kind string, o callOptions) *trace {
	if !o.eval {
		return nil
	}
	return &trace{b: atif.NewBuilder(atif.Agent{Name: "gligabao-" + kind, Version: agentVersion}), dir: o.dir}
}

// wrap returns llm with every call recorded as steps of the trajectory.
func (t *trace) wrap(llm LLM) LLM {
	if t == nil {
		return llm
	}
	return &tracingLLM{LLM: llm, b: t.b}
}

func (t *trace) user(message string) {
	if t != nil {
		t.b.User(message)
	}
}

func (t *trace) agent(message string) {
	if t != nil {
		t.b.Agent(atif.Step{Message: message})
	}
}

func (t *trace) input(v map[string]any) {
	if t != nil {
		t.b.SetExtra("input", v)
	}
}

// finish stores the trajectory and returns it (nil if there is no trace). A failed call is stored too (with the
// error in the notes), but the caller only sees the trajectory of a successful one. Failing to store the
// trajectory never fails the call.
func (t *trace) finish(callErr error) *atif.Trajectory {
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

// tool records a tool step whose arguments and result are summarized from Go values.
func (t *trace) tool(message, name string, args map[string]any, result any) {
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

// embed records an embedding request as a tool step with its tokens and cost, so they count in the trajectory's totals.
func (t *trace) embed(texts []string, entities, edges int, usage *ai.EmbedUsage) {
	t.embedStep("Embedded the entities and edges stored by this artifact.", "embed",
		map[string]any{"entities": entities, "edges": edges, "texts": texts}, map[string]any{"vectors": len(texts)}, usage)
}

// searchEntities records the similarity search that adds candidate facts: the embedded endpoints and what they matched.
func (t *trace) searchEntities(texts []string, found []similarEntity, facts []map[string]any, usage *ai.EmbedUsage, err error) {
	if t == nil || len(texts) == 0 {
		return
	}
	matches := make([]map[string]any, 0, len(found))
	for _, f := range found {
		matches = append(matches, map[string]any{"endpoint": f.endpoint, "entity": f.name, "type": f.typ, "score": f.score})
	}
	res := map[string]any{"matches": matches, "candidates": len(facts), "facts": facts}
	if err != nil {
		res["error"] = err.Error() // not fatal: the candidates then come from the exact names only
	}
	t.embedStep("Embedded the endpoints of the new facts and searched the dataset's entity index for similar stored "+
		"entities (aliases); their live facts are the candidates found by similarity.", "search_similar_entities",
		map[string]any{"texts": texts, "top_k": similarEntityTopK, "min_score": similarEntityMinScore}, res, usage)
}

func (t *trace) embedStep(message, name string, args, res map[string]any, usage *ai.EmbedUsage) {
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
