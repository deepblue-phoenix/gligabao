package agent

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"

	log "github.com/sirupsen/logrus"
)

// This file records a Run as an ATIF trajectory (package atif), the way the memory pipelines do for Remember and Query: it is opt-in
// (Agent.Trajectory), the trajectory is returned in the Result (and, with Agent.TrajectoryDir, stored as JSON), and a failed run
// gets one too, with the error in its notes. A nil *tracer (not requested) and a nil *stepRec record nothing, so the loop needs no checks.

const (
	defaultAgentName = "gligabao-agent"
	agentVersion     = "0.1"
)

// tracer records one Run.
type tracer struct {
	b     *atif.Builder
	dir   string
	model string // the model of the first answer, when the Agent did not name one
}

// newTracer returns nil unless the agent asks for a trajectory. It records the system prompt and the user's prompt, the first steps of the
// trajectory.
func newTracer(a *Agent, prompt string, l *loop) *tracer {
	if !a.Trajectory {
		return nil
	}
	name := a.Name
	if name == "" {
		name = defaultAgentName
	}
	version := a.Version
	if version == "" {
		version = agentVersion
	}
	t := &tracer{b: atif.NewBuilder(atif.Agent{Name: name, Version: version, ModelName: a.Model}), dir: a.TrajectoryDir}
	if a.SessionID != "" { // the trajectories of one session share the session ID
		t.b.SetSessionID(a.SessionID)
	}
	names := make([]string, 0, len(a.Tools))
	for _, tool := range a.Tools {
		names = append(names, tool.Name)
	}
	t.b.SetExtra("input", map[string]any{"max_steps": l.maxSteps, "max_tokens": l.maxTokens, "tools": names})
	if a.System != "" {
		t.b.System(a.System)
	}
	t.b.User(prompt)
	return t
}

// stepRec is one model call of the run: its answer, the tools it asked for and what they returned. It becomes one agent step.
type stepRec struct {
	step    atif.Step
	failed  []string           // IDs of the tool calls that failed
	latency map[string]float64 // tool call ID -> milliseconds
	tools   struct {           // what the tools of the step spent on their own (see ReportUsage)
		in, out int
		cost    float64
	}
}

// beginStep starts the record of a model call that started at started and got resp.
func (t *tracer) beginStep(started time.Time, resp *ai.RawResponse) *stepRec {
	if t == nil {
		return nil
	}
	if t.model == "" {
		t.model = resp.Model
	}
	return &stepRec{
		latency: map[string]float64{},
		step: atif.Step{
			Timestamp: started.UTC().Format(time.RFC3339Nano),
			ModelName: resp.Model,
			Metrics:   &atif.Metrics{PromptTokens: resp.InputTokens, CompletionTokens: resp.OutputTokens, CostUSD: resp.CostUSD},
			Extra: map[string]any{
				"provider":    "anthropic",
				"stop_reason": resp.StopReason,
				"latency_ms":  float64(time.Since(started).Microseconds()) / 1000,
			},
		},
	}
}

// call records a tool call the model asked for.
func (s *stepRec) call(id, name string, input json.RawMessage) {
	if s == nil {
		return
	}
	args := map[string]any{}
	if err := json.Unmarshal(input, &args); err != nil || args == nil {
		args = map[string]any{"_raw": string(input)} // not an object: keep what the model sent
	}
	s.step.ToolCalls = append(s.step.ToolCalls, atif.ToolCall{ToolCallID: id, FunctionName: name, Arguments: args})
}

// result records what a tool call returned (the error text if isErr), how long it took and what it spent on its own.
func (s *stepRec) result(id, out string, isErr bool, took time.Duration, in, outTokens int, cost float64) {
	if s == nil {
		return
	}
	if s.step.Observation == nil {
		s.step.Observation = &atif.Observation{}
	}
	s.step.Observation.Results = append(s.step.Observation.Results, atif.ObservationResult{SourceCallID: id, Content: out})
	if isErr {
		s.failed = append(s.failed, id)
	}
	s.latency[id] = float64(took.Microseconds()) / 1000
	s.tools.in += in
	s.tools.out += outTokens
	s.tools.cost += cost
}

// endStep adds the step to the trajectory, with the text the model wrote in it.
func (t *tracer) endStep(s *stepRec, text string) {
	if t == nil || s == nil {
		return
	}
	s.step.Message = strings.TrimSpace(text)
	if len(s.failed) > 0 {
		s.step.Extra["failed_tool_calls"] = s.failed
	}
	if len(s.latency) > 0 {
		s.step.Extra["tool_latency_ms"] = s.latency
	}
	if s.tools.in > 0 || s.tools.out > 0 || s.tools.cost > 0 {
		// Kept apart from the step's metrics (those are the model call's own); the totals over the run are in the trajectory's extra.
		s.step.Extra["tools_usage"] = map[string]any{"input_tokens": s.tools.in, "output_tokens": s.tools.out, "cost_usd": s.tools.cost}
	}
	t.b.Agent(s.step)
}

// finish ends the trajectory: it adds the run's totals (the tools' spending included, which the final metrics, the model calls only,
// do not have) and the error if the run failed, stores it if there is a directory, and puts it in res. A failure to store it never fails
// the run.
func (t *tracer) finish(res *Result, runErr error) {
	if t == nil {
		return
	}
	if runErr != nil {
		t.b.SetNotes("failed: " + runErr.Error())
	}
	if res != nil {
		t.b.SetExtra("result", map[string]any{
			"steps": res.Steps, "tool_calls": res.ToolCalls, "input_tokens": res.InputTokens, "output_tokens": res.OutputTokens,
			"cost_usd": res.CostUSD, "answer": res.Answer,
		})
	}
	traj := t.b.Build()
	if traj.Agent.ModelName == "" {
		traj.Agent.ModelName = t.model
	}
	if res == nil {
		return
	}
	res.Trajectory = traj
	if t.dir == "" {
		return
	}
	path, err := atif.WriteFile(t.dir, traj)
	if err != nil {
		log.WithError(err).WithField("trajectory_id", traj.TrajectoryID).Error("store ATIF trajectory")
		return
	}
	res.TrajectoryPath = path
	log.WithFields(log.Fields{"trajectory_id": traj.TrajectoryID, "path": path}).Debug("ATIF trajectory stored")
}
