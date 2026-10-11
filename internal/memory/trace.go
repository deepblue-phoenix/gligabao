package memory

import (
	"context"
	"encoding/json"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"

	log "github.com/sirupsen/logrus"
)

// TrajectoryDir is where Remember and Recall store their ATIF trajectories (one pretty-printed JSON file per
// call, named <trajectory_id>.json). It is read on every call; set it once at startup, or override it per call
// with WithTrajectoryDir.
var TrajectoryDir = "bin/atif"

const agentVersion = "0.1"

//---------------------------------------------------------------------------------------------

// Option customizes a single Remember or Recall call.
type Option func(*callOptions)

type callOptions struct {
	eval bool
	dir  string
}

func newCallOptions(opts []Option) callOptions {
	o := callOptions{dir: TrajectoryDir}
	for _, f := range opts {
		f(&o)
	}
	return o
}

// WithEval makes the call also return its ATIF trajectory (Result.Trajectory / RecallResult.Trajectory).
// It is meant for evals and is only available to direct Go callers; the HTTP endpoints never set it.
func WithEval() Option { return func(o *callOptions) { o.eval = true } }

// WithTrajectoryDir overrides TrajectoryDir for this call.
func WithTrajectoryDir(dir string) Option { return func(o *callOptions) { o.dir = dir } }

//---------------------------------------------------------------------------------------------

// trace records one Remember/Recall call as an ATIF trajectory.
type trace struct {
	b *atif.Builder
	o callOptions
}

func startTrace(kind string, o callOptions) *trace {
	return &trace{b: atif.NewBuilder(atif.Agent{Name: "gligabao-" + kind, Version: agentVersion}), o: o}
}

// wrap returns llm with every call recorded as steps of the trajectory.
func (t *trace) wrap(llm LLM) LLM { return &tracingLLM{LLM: llm, b: t.b} }

// finish stores the trajectory and returns its ID, plus the trajectory itself if eval was requested. A failed
// call is stored too (with the error in the notes), but the caller only sees the ID of a successful one.
// Failing to store the trajectory never fails the call.
func (t *trace) finish(callErr error) (string, *atif.Trajectory) {
	if callErr != nil {
		t.b.SetNotes("failed: " + callErr.Error())
	}
	traj := t.b.Build()
	if path, err := atif.WriteFile(t.o.dir, traj); err != nil {
		log.WithError(err).WithField("trajectory_id", traj.TrajectoryID).Error("store ATIF trajectory")
	} else {
		log.WithFields(log.Fields{"trajectory_id": traj.TrajectoryID, "path": path}).Debug("ATIF trajectory stored")
	}
	if t.o.eval {
		return traj.TrajectoryID, traj
	}
	return traj.TrajectoryID, nil
}

// tool records a tool step whose arguments and result are summarized from Go values.
func (t *trace) tool(message, name string, args map[string]any, result any) {
	res, ok := result.(string)
	if !ok {
		b, _ := json.Marshal(result)
		res = string(b)
	}
	t.b.Tool(message, name, args, res)
}

//---------------------------------------------------------------------------------------------

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
		l.b.User(m.Content)
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
