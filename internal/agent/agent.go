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

	"github.com/deepblue-phoenix/gligabao/internal/ai"
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
	Run func(ctx context.Context, input json.RawMessage) (string, error)
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
}

// Observer receives the progress of a Run. Every field is optional. The functions are called on the goroutine that runs the agent,
// one at a time and in the order the events happen, so they must be quick (a database write is fine) and must not call back into
// the agent.
type Observer struct {
	// BeforeModel is called before each model call. step counts from 1; final says that this is the last step the agent may take,
	// so the model is not allowed to call tools in it and its text is the answer.
	BeforeModel func(step int, final bool)
	// AfterModel is called after each successful model call, with the totals of the run so far.
	AfterModel func(step int, total Result)
	// BeforeTool is called before a tool runs, with the input the model chose.
	BeforeTool func(step int, name string, input json.RawMessage)
	// AfterTool is called after a tool ran, with its output (the error text if isErr) and the totals of the run so far, which
	// include what the tool itself spent (see ReportUsage).
	AfterTool func(step int, name, output string, isErr bool, total Result)
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
func ReportUsage(ctx context.Context, inputTokens, outputTokens int, costUSD float64) {
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
}

// Run gives the prompt to the model and loops until it answers without calling a tool.
//
// Every Run is isolated: all that changes while it works (the conversation, the totals, what the tools spent) lives in a loop made
// for this call alone, and the Agent is only read (its settings are copied when the run starts). So Runs, even of the same Agent
// value, share nothing, and can go on at the same time on their own goroutines.
func (a *Agent) Run(ctx context.Context, prompt string) (*Result, error) {
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
	tools     map[string]Tool
	specs     []any

	// What changes while it runs.
	spent    usage // what the tools of the current step spent on their own
	messages []any
	res      Result
}

// newLoop makes the state of a Run of a, with the defaults filled in.
func newLoop(a *Agent, prompt string) *loop {
	l := &loop{
		client:    a.Client,
		system:    a.System,
		model:     a.Model,
		maxSteps:  a.MaxSteps,
		maxTokens: a.MaxTokens,
		obs:       a.Observer,
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
	return l
}

// run is the loop itself.
func (l *loop) run(ctx context.Context) (*Result, error) {
	ctx = context.WithValue(ctx, usageKey{}, &l.spent)
	obs := l.obs
	res := &l.res
	for res.Steps < l.maxSteps {
		res.Steps++
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
			}
		}
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

		// Collect the text, and run the tools the model asked for.
		var text strings.Builder
		var results []any
		for _, raw := range resp.Content {
			var b struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(raw, &b); err != nil {
				return res, fmt.Errorf("agent: decode content block: %w", err)
			}
			switch b.Type {
			case "text":
				text.WriteString(b.Text)
			case "tool_use":
				res.ToolCalls++
				if obs.BeforeTool != nil {
					obs.BeforeTool(res.Steps, b.Name, b.Input)
				}
				out, isErr := runTool(ctx, l.tools, b.Name, b.Input)
				in, outTokens, cost := l.spent.drain()
				res.InputTokens += in
				res.OutputTokens += outTokens
				res.CostUSD += cost
				if obs.AfterTool != nil {
					obs.AfterTool(res.Steps, b.Name, out, isErr, *res)
				}
				results = append(results, map[string]any{
					"type": "tool_result", "tool_use_id": b.ID, "content": out, "is_error": isErr,
				})
			}
		}
		res.Answer = strings.TrimSpace(text.String())
		if len(results) == 0 {
			return res, nil
		}
		l.messages = append(l.messages, map[string]any{"role": "user", "content": results})
	}
	return res, nil
}

// runTool runs one tool call; failures (unknown tool, tool error) become the text the model reads.
func runTool(ctx context.Context, tools map[string]Tool, name string, input json.RawMessage) (out string, isErr bool) {
	defer func() {
		if p := recover(); p != nil { // a tool that panics must not take the whole run down
			out, isErr = fmt.Sprintf("tool %s failed: %v", name, p), true
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
