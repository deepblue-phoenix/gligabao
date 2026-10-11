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

// Option customizes a single Remember or Recall call. Without WithEval no trajectory is generated.
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

// WithEval makes the call generate an ATIF trajectory: it is stored in TrajectoryDir and returned, with its ID, in
// Result / RecallResult (TrajectoryID, Trajectory). Without it nothing is generated. It is meant for evals; only the
// eval HTTP endpoints set it.
func WithEval() Option { return func(o *callOptions) { o.eval = true } }

// WithTrajectoryDir overrides TrajectoryDir for this call.
func WithTrajectoryDir(dir string) Option { return func(o *callOptions) { o.dir = dir } }

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
