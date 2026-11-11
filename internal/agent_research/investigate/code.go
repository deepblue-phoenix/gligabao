package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate_core"
	"github.com/deepblue-phoenix/gligabao/internal/monty"
	"github.com/deepblue-phoenix/gligabao/internal/monty/hostfn"
)

// This file is the run_code tool of the loop (docs/agents/monty-code-execution.md): a Python program run in the sandbox, with the host
// functions of a hostfn.Registry. The description, the environment of the program and the formatting of the result are pure functions; the
// call into the sandbox and the fetch class functions are at the edge, on the runner.

// CodeConfig turns the run_code tool on for a run (Config.Code).
type CodeConfig struct {
	// Runner runs the programs (a *monty.Sidecar).
	Runner monty.Runner
	// Settings are the limits of the [code] table (defaults are filled in by Run).
	Settings agent_defs.Code
	// Registry is the functions the program may call; the ones that need a tool the agent does not have are dropped by Run.
	Registry hostfn.Registry
}

// ToolRunCode is the name of the code tool.
const ToolRunCode = investigate_core.ToolRunCode

// CodeToolSpec is the description and the input schema of run_code; the description ends with the list of functions of the registry.
func CodeToolSpec(reg hostfn.Registry) (description string, schema map[string]any) {
	description = "Run a short Python program in a sandbox (Monty, a subset of Python: no imports of third-party packages, no files, no network). " +
		"Use it for exact arithmetic, unit conversion, counting, sorting, parsing or filtering text you already have, and comparing numbers. " +
		"Print what you want to read; only the printed output and the value of the last expression come back. " +
		"A run is stateless: variables do not survive to the next call. It cannot change the frontier, the notes or the claims."
	if reg.Len() > 0 {
		description += "\n\nFunctions you may call:\n" + reg.Describe()
	}
	schema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"code":   map[string]any{"type": "string", "description": "the Python program"},
			"inputs": map[string]any{"type": "object", "description": "named JSON values bound as variables before the program runs (e.g. a table you parsed)"},
		},
		"required": []string{"code"},
	}
	return description, schema
}

// EnvOf is the read-only view of the state for one run_code call (no effects: the caller adds Fetch and Log).
func EnvOf(st investigate_core.State) hostfn.Env {
	qs := make([]map[string]any, len(st.Frontier.Questions))
	for i, q := range st.Frontier.Questions {
		attempts := make([]map[string]any, len(q.Attempts))
		for j, a := range q.Attempts {
			attempts[j] = map[string]any{"seq": a.Seq, "query": a.Query, "tool": a.Tool, "outcome": a.Outcome}
		}
		qs[i] = map[string]any{
			"id": q.ID, "parent_id": q.ParentID, "question": q.Question, "status": string(q.Status), "priority": q.Priority,
			"answer": q.Answer, "validation_criteria": q.ValidationCriteria, "attempts": attempts,
		}
	}
	claim := func(c investigate_core.Claim) map[string]any {
		srcs := make([]string, len(c.Links))
		for i, l := range c.Links {
			srcs[i] = l.SourceID
		}
		return map[string]any{"id": c.ID, "kind": string(c.Kind), "text": c.Text, "status": string(c.Status), "question_id": c.QuestionID,
			"sources": srcs, "value": c.Value, "unit": c.Unit}
	}
	var cs []map[string]any
	for _, c := range st.Notes.Claims {
		cs = append(cs, claim(c))
	}
	for _, c := range st.Notes.Datapoints {
		cs = append(cs, claim(c))
	}
	if cs == nil {
		cs = []map[string]any{}
	}
	return hostfn.Env{Notes: investigate_core.Render(st.Notes), Questions: qs, Claims: cs}
}

// FormatCodeResult is what the model reads of a run: "ok", the value and the stdout, or "error: kind: message"; capped at max chars
// (0: no cap) with a [truncated N chars] marker. isErr says that the tool call is an error.
func FormatCodeResult(res monty.Result, err error, max int) (text string, isErr bool) {
	if err != nil {
		return capChars("error: "+err.Error(), max), true
	}
	var b strings.Builder
	b.WriteString("ok")
	if res.Value != nil {
		v, _ := json.Marshal(res.Value)
		b.WriteString("\nvalue: " + string(v))
	}
	if out := strings.TrimRight(res.Stdout, "\n"); out != "" {
		b.WriteString("\nstdout:\n" + out)
	}
	return capChars(b.String(), max), false
}

func capChars(s string, max int) string {
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s
	}
	return string(r[:max]) + fmt.Sprintf("\n[truncated %d chars]", len(r)-max)
}

// code is the state of the code tool of one run.
type code struct {
	cfg   CodeConfig
	reg   hostfn.Registry
	calls int // run_code calls so far
}

// newCode returns the code tool state of the run, with the registry narrowed to the functions the agent can serve; nil if code is off.
func newCode(cfg *CodeConfig, external []agent.Tool) (*code, error) {
	if cfg == nil || cfg.Runner == nil {
		return nil, nil
	}
	has := func(tool string) bool {
		for _, t := range external {
			if t.Name == tool {
				return true
			}
		}
		return false
	}
	reg, err := cfg.Registry.Only(cfg.Settings.Functions)
	if err != nil {
		return nil, err
	}
	reg = reg.Filter(func(s hostfn.Spec) bool { t := hostfn.ToolOf(s.Name); return t == "" || has(t) })
	c := *cfg
	c.Settings = c.Settings.WithDefaults()
	return &code{cfg: c, reg: reg}, nil
}

func (c *code) tool(r *runner) agent.Tool {
	desc, schema := CodeToolSpec(c.reg)
	return agent.Tool{Name: ToolRunCode, Description: desc, Schema: schema, Run: func(ctx context.Context, input json.RawMessage) (string, error) {
		return r.runCode(ctx, input)
	}}
}

// externals are the tools of the agent and, when code is on, run_code.
func (r *runner) externals() []agent.Tool {
	if r.code == nil {
		return r.cfg.External
	}
	return append(append([]agent.Tool{}, r.cfg.External...), r.code.tool(r))
}

// runCode is the run_code tool: one program in a fresh sandbox session.
func (r *runner) runCode(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Code   string         `json:"code"`
		Inputs map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if strings.TrimSpace(in.Code) == "" {
		return "", errors.New(`missing "code"`)
	}
	s := r.code.cfg.Settings
	if s.MaxCalls != nil && *s.MaxCalls > 0 && r.code.calls >= *s.MaxCalls {
		return "", fmt.Errorf("run_code was used %d times, the most this run may: work with what you have", r.code.calls)
	}
	r.code.calls++
	st, err := r.sess.Load(ctx)
	if err != nil {
		return "", err
	}
	run := &codeTrace{code: in.Code}
	env := EnvOf(st)
	env.Fetch = func(ctx context.Context, tool string, in map[string]any) (string, error) {
		return r.codeFetch(ctx, st, tool, in)
	}
	env.Log = func(line string) { run.logs = append(run.logs, line) }
	host := func(ctx context.Context, name string, args []any, kwargs map[string]any) (any, error) {
		run.calls = append(run.calls, map[string]any{"function": name, "args": args, "kwargs": kwargs})
		if len(run.calls) > s.MaxHostCalls {
			return nil, fmt.Errorf("BudgetExceeded: at most %d host function calls per run_code call", s.MaxHostCalls)
		}
		return r.code.reg.Call(ctx, env, name, args, kwargs, s.MaxResultChars)
	}
	res, err := r.code.cfg.Runner.Run(ctx, monty.Request{
		Code: in.Code, Inputs: in.Inputs, Functions: r.code.reg.Names(), Stubs: r.code.reg.Stubs(), TypeCheck: s.TypeCheck,
		Limits: monty.Limits{MaxDurationS: float64(s.TimeoutS), MaxMemoryMB: s.MaxMemoryMB, MaxRecursion: s.MaxRecursion},
	}, host)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	text, isErr := FormatCodeResult(res, err, s.MaxOutputChars)
	run.output, run.stats = text, res.Stats
	r.rec.codeRun(run)
	if isErr {
		return "", errors.New(text)
	}
	return text, nil
}

// codeFetch runs a tool of the agent for a fetch class function: through runExternal, so that usage and cost are counted, the tool's own
// limits and domain rules apply and a raw tool's call is logged as an attempt of the current question.
func (r *runner) codeFetch(ctx context.Context, st investigate_core.State, tool string, in map[string]any) (string, error) {
	for _, t := range r.cfg.External {
		if t.Name != tool {
			continue
		}
		input, err := json.Marshal(in)
		if err != nil {
			return "", err
		}
		out, isErr := r.runExternal(ctx, t, st, investigate_core.Call{ID: "code", Name: tool, Input: input})
		if isErr {
			return "", errors.New(out)
		}
		return out, nil
	}
	return "", fmt.Errorf("the tool %s is not available", tool)
}

// codeTrace is what the trajectory records of one run_code call.
type codeTrace struct {
	code   string
	output string
	stats  monty.Stats
	calls  []map[string]any
	logs   []string
}

func (t *codeTrace) extra() map[string]any {
	return map[string]any{"code": t.code, "output": t.output, "wall_ms": t.stats.WallMS, "host_calls": t.calls, "log": t.logs}
}

func (s *stepRec) codeRun(t *codeTrace) {
	if s == nil {
		return
	}
	prev, _ := s.step.Extra["code_runs"].([]map[string]any)
	s.step.Extra["code_runs"] = append(prev, t.extra())
}
