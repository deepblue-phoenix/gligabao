// Package agent is a very small tool-calling agent: no framework, just a loop. The model is called with a list of
// tools; while it asks for tools, they are run and their results are sent back; when it stops asking, its text is
// the answer. See research.go for a deep-research agent built on it.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	gberrors "github.com/deepblue-phoenix/gligabao/internal/sys/errors"
)

const (
	defaultMaxSteps  = 10
	defaultMaxTokens = 4096
)

// Tool is something the model can call.
type Tool struct {
	Name        string
	Description string
	// Schema is the JSON schema of the input (an object).
	Schema map[string]any
	// Run gets the input the model chose (JSON, as the schema says) and returns the text the model will read. An
	// error is not fatal: the model is told about it and may try something else.
	Run func(ctx context.Context,
		input json.RawMessage) (string, error)
}

// Agent is a model with tools and a system prompt. The zero values of the optional fields use defaults.
type Agent struct {
	Client *ai.Client
	System string
	Tools  []Tool
	// Model is the model to use; empty uses the client's default.
	Model string
	// MaxTokens bounds each model answer; 0 means 4096.
	MaxTokens int
	// MaxSteps is the most model calls in a run; 0 means 10. The last one is not given the chance to call tools, so
	// the run always ends with an answer.
	MaxSteps int
	// Observer is told about the progress of a Run, step by step; the zero value observes nothing.
	Observer Observer
	// OutOfBudget, if set, is asked before each model call whether the run's budget is spent: it returns why ("" if not). When it
	// says so, that call is the last, as if the steps had run out: the model is told, may not call tools, and its text is the answer
	// (Result.OutOfBudget says why the run was cut short). The answer of a question that stays "" is never changed.
	OutOfBudget func() string
	// Terminal, if set, is the name of one of the Tools that ends the run: when the model calls it and the tool succeeds, the run is
	// over and its input is in Result.Final (an error of the tool goes back to the model, which may fix it). A model that answers in
	// text without calling it is told to call it; the last step is forced to call it (tool_choice) instead of being tool-free.
	Terminal string

	// Trajectory makes Run record the run as an ATIF trajectory (see package atif), returned in Result.Trajectory (also for a run that
	// failed). Off by default: nothing is recorded.
	Trajectory bool
	// TrajectoryDir, with Trajectory, also stores the trajectory there as <trajectory_id>.json (Result.TrajectoryPath); empty means
	// it is only returned.
	TrajectoryDir string
	// Name is the name of the agent in the trajectory; empty means "gligabao-agent". Version is its version there; empty means "0.1".
	Name    string
	Version string
	// SessionID is the session ID of the trajectory, so that what one session did can be connected; empty means the trajectory's own ID.
	SessionID string
	// ErrSink stores the GBerrors of the runs (a tool that panics, a trajectory that could not be stored); nil stores none.
	ErrSink gberrors.Sink
}

// Observer receives the progress of a Run. Every field is optional. The functions are called on the goroutine that runs the agent,
// one at a time and in the order the events happen, so they must be quick (a database write is fine) and must not call back into
// the agent.
type Observer struct {
	// BeforeModel is called before each model call. step counts from 1; final says that this is the last step the agent may take,
	// so the model is not allowed to call tools in it and its text is the answer.
	BeforeModel func(step int,
		final bool)
	// AfterModel is called after each successful model call, with the totals of the run so far.
	AfterModel func(step int,
		total Result)
	// BeforeTool is called before a tool runs, with the input the model chose.
	BeforeTool func(step int,
		name string,
		input json.RawMessage)
	// AfterTool is called after a tool ran, with its output (the error text if isErr) and the totals of the run so far, which
	// include what the tool itself spent (see ReportUsage).
	AfterTool func(step int,
		name,
		output string,
		isErr bool,
		total Result)
}

// usageKey is the context key of the usage a tool reports.
type usageKey struct{}

// usage is what the tools of a step spent on their own (the model calls they make).
type usage struct {
	mu                        sync.Mutex
	inputTokens, outputTokens int
	costUSD                   float64
}

// drain returns what was reported since the last call, and resets it.
func (u *usage) drain() (in, out int, cost float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	in, out, cost = u.inputTokens, u.outputTokens, u.costUSD
	u.inputTokens, u.outputTokens, u.costUSD = 0, 0, 0
	return in, out, cost
}

// ReportUsage lets a tool that calls a model itself (a web search, a summary, ...) tell the agent what it spent, so that it is added
// to the Result of the run. ctx must be the one the tool was run with; with any other it does nothing.
func ReportUsage(ctx context.Context,
	inputTokens,
	outputTokens int,
	costUSD float64) {
	u, ok := ctx.Value(usageKey{}).(*usage)
	if !ok {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.inputTokens += inputTokens
	u.outputTokens += outputTokens
	u.costUSD += costUSD
}

// WithUsage returns a context that collects what the tools run with it report (ReportUsage), and a function that returns what was
// collected since its last call and resets it. For a loop that runs the tools itself (the investigative loop of package
// agent_research/investigate); the Agent's own loop does this by itself.
func WithUsage(ctx context.Context) (context.Context, func() (inputTokens, outputTokens int, costUSD float64)) {
	u := &usage{}
	return context.WithValue(ctx,
		usageKey{},
		u), u.drain
}

// Result is the outcome of a Run.
type Result struct {
	// Answer is the text of the model's last turn.
	Answer string
	// Steps is the number of model calls; ToolCalls the number of tool runs.
	Steps     int
	ToolCalls int
	// InputTokens, OutputTokens and CostUSD are summed over all the model calls, those of the tools included (see ReportUsage).
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	// Final is the input of the call of Agent.Terminal that ended the run (nil if it did not end that way).
	Final json.RawMessage
	// OutOfBudget is why the run was cut short by Agent.OutOfBudget ("" if it was not): the last call was made early, without tools.
	OutOfBudget string
	// Trajectory is the ATIF trajectory of the run, when the Agent asked for one (Agent.Trajectory); nil otherwise.
	Trajectory *atif.Trajectory
	// TrajectoryPath is the file it was stored in (Agent.TrajectoryDir); empty when it was not stored.
	TrajectoryPath string
}

// Run gives the prompt to the model and loops until it answers without calling a tool.
//
// Every Run is isolated: all that changes while it works (the conversation, the totals, what the tools spent) lives in a loop made
// for this call alone, and the Agent is only read (its settings are copied when the run starts). So Runs, even of the same Agent
// value, share nothing, and can go on at the same time on their own goroutines.
func (a *Agent) Run(ctx context.Context,
	prompt string) (*Result, error) {
	return newLoop(a, prompt).run(ctx)
}

// loop is the whole state of one Run: the settings it was started with, the conversation and the totals. Nothing in it is shared
// with any other Run.
type loop struct {
	// Settings, fixed when the loop is made.
	client    *ai.Client
	system    string
	model     string
	maxSteps  int
	maxTokens int
	obs       Observer
	budget    func() string
	terminal  string
	tools     map[string]Tool
	specs     []any
	errSink   gberrors.Sink

	// What changes while it runs.
	trace    *tracer // nil unless a trajectory was asked for
	spent    usage   // what the tools of the current step spent on their own
	messages []any
	res      Result
}

// newLoop makes the state of a Run of a, with the defaults filled in.
func newLoop(a *Agent,
	prompt string) *loop {
	l := &loop{
		client:    a.Client,
		system:    a.System,
		model:     a.Model,
		maxSteps:  a.MaxSteps,
		maxTokens: a.MaxTokens,
		obs:       a.Observer,
		budget:    a.OutOfBudget,
		terminal:  a.Terminal,
		errSink:   a.ErrSink,
		tools:     map[string]Tool{},
		messages:  []any{map[string]any{"role": "user", "content": prompt}},
	}
	if l.maxSteps <= 0 {
		l.maxSteps = defaultMaxSteps
	}
	if l.maxTokens <= 0 {
		l.maxTokens = defaultMaxTokens
	}
	for _, t := range a.Tools {
		l.tools[t.Name] = t
		l.specs = append(l.specs, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Schema})
	}
	l.trace = newTracer(a,
		prompt,
		l)
	return l
}

// run is the loop itself, and the trajectory of it, if there is one.
func (l *loop) run(ctx context.Context) (*Result, error) {
	res, err := l.steps(ctx)
	l.trace.finish(ctx,
		res,
		err)
	return res, err
}

// steps goes through the model calls and tool runs.
func (l *loop) steps(ctx context.Context) (*Result, error) {
	ctx = context.WithValue(ctx,
		usageKey{},
		&l.spent)
	obs := l.obs
	res := &l.res
	for res.Steps < l.maxSteps {
		res.Steps++
		if res.Steps < l.maxSteps && l.budget != nil {
			if reason := l.budget(); reason != "" {
				// The budget is spent: this call is the last, as if the steps had run out, and the model is told why.
				l.maxSteps = res.Steps
				res.OutOfBudget = reason
				l.tellBudget(reason)
			}
		}
		if obs.BeforeModel != nil {
			obs.BeforeModel(res.Steps, res.Steps == l.maxSteps)
		}
		payload := map[string]any{"max_tokens": l.maxTokens, "messages": l.messages}
		if l.model != "" {
			payload["model"] = l.model
		}
		if l.system != "" {
			payload["system"] = l.system
		}
		if len(l.specs) > 0 {
			payload["tools"] = l.specs
			if res.Steps == l.maxSteps {
				payload["tool_choice"] = map[string]any{"type": "none"}
				if l.terminal != "" {
					payload["tool_choice"] = map[string]any{"type": "tool", "name": l.terminal}
				}
			}
		}
		started := time.Now()
		resp, err := l.client.Raw(ctx, payload)
		if err != nil {
			return res, err
		}
		res.InputTokens += resp.InputTokens
		res.OutputTokens += resp.OutputTokens
		res.CostUSD += resp.CostUSD
		l.messages = append(l.messages, map[string]any{"role": "assistant", "content": resp.Content})
		if obs.AfterModel != nil {
			obs.AfterModel(res.Steps, *res)
		}

		rec := l.trace.beginStep(started, resp)

		// Collect the text, and run the tools the model asked for.
		var text strings.Builder
		var results []any
		var final json.RawMessage
		for _, raw := range resp.Content {
			var b struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(raw, &b); err != nil {
				l.trace.endStep(rec, text.String())
				return res, fmt.Errorf("agent: decode content block: %w", err)
			}
			switch b.Type {
			case "text":
				text.WriteString(b.Text)
			case "tool_use":
				res.ToolCalls++
				if obs.BeforeTool != nil {
					obs.BeforeTool(res.Steps,
						b.Name,
						b.Input)
				}
				rec.call(b.ID,
					b.Name,
					b.Input)
				toolStarted := time.Now()
				out, isErr := runTool(ctx,
					l.tools,
					l.errSink,
					b.Name,
					b.Input)
				in, outTokens, cost := l.spent.drain()
				rec.result(b.ID,
					out,
					isErr,
					time.Since(toolStarted),
					in,
					outTokens,
					cost)
				res.InputTokens += in
				res.OutputTokens += outTokens
				res.CostUSD += cost
				if obs.AfterTool != nil {
					obs.AfterTool(res.Steps,
						b.Name,
						out,
						isErr,
						*res)
				}
				if l.terminal != "" && b.Name == l.terminal && !isErr {
					final = b.Input
				}
				results = append(results, map[string]any{
					"type": "tool_result", "tool_use_id": b.ID, "content": out, "is_error": isErr,
				})
			}
		}
		res.Answer = strings.TrimSpace(text.String())
		l.trace.endStep(rec, text.String())
		if final != nil {
			res.Final = final
			return res, nil
		}
		if len(results) == 0 {
			if l.terminal != "" && res.Steps < l.maxSteps {
				l.messages = append(l.messages, map[string]any{"role": "user", "content": "Call " + l.terminal + " now to end your work."})
				continue
			}
			return res, nil
		}
		l.messages = append(l.messages, map[string]any{"role": "user", "content": results})
	}
	return res, nil
}

// tellBudget adds to the user turn the model is about to answer (the results of its tools) that the budget is spent.
func (l *loop) tellBudget(reason string) {
	if l.terminal != "" {
		l.tellBudgetTerminal(reason)
		return
	}
	note := map[string]any{"type": "text", "text": "[Budget reached] " + reason + " This is your last turn: you cannot call tools any more. " +
		"Write your final answer now, from what you have found so far, and say plainly what you could not cover."}
	last, ok := l.messages[len(l.messages)-1].(map[string]any)
	if !ok || last["role"] != "user" {
		return
	}
	if blocks, ok := last["content"].([]any); ok {
		last["content"] = append(blocks, note)
	}
}

// runTool runs one tool call; failures (unknown tool, tool error) become the text the model reads.
func runTool(ctx context.Context,
	tools map[string]Tool,
	sink gberrors.Sink,
	name string,
	input json.RawMessage) (out string, isErr bool) {
	defer func() {
		if p := recover(); p != nil { // a tool that panics must not take the whole run down (and is a bug: tracked)
			gberrors.New(context.WithoutCancel(ctx),
				sink,
				"agent.tool.panic",
				fmt.Errorf("%v", p),
				gberrors.Meta{"tool": name})
			out, isErr = fmt.Sprintf("tool %s failed: %v",
				name,
				p), true
		}
	}()
	t, ok := tools[name]
	if !ok {
		return "unknown tool " + name, true
	}
	out, err := t.Run(ctx, input)
	if err != nil {
		return err.Error(), true
	}
	return out, false
}

// tellBudgetTerminal is tellBudget for an agent that ends with its Terminal tool.
func (l *loop) tellBudgetTerminal(reason string) {
	note := map[string]any{"type": "text", "text": "[Budget reached] " + reason + " This is your last turn: call " + l.terminal + " now with what you have found so far, and say plainly what you could not cover."}
	last, ok := l.messages[len(l.messages)-1].(map[string]any)
	if !ok || last["role"] != "user" {
		return
	}
	if blocks, ok := last["content"].([]any); ok {
		last["content"] = append(blocks, note)
	}
}
