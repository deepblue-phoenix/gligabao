// Package agent is a very small tool-calling agent: no framework, just a loop. The model is called with a list of
// tools; while it asks for tools, they are run and their results are sent back; when it stops asking, its text is
// the answer. See research.go for a deep-research agent built on it.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
}

// Result is the outcome of a Run.
type Result struct {
	// Answer is the text of the model's last turn.
	Answer string
	// Steps is the number of model calls; ToolCalls the number of tool runs.
	Steps     int
	ToolCalls int
	// InputTokens, OutputTokens and CostUSD are summed over all the model calls.
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// Run gives the prompt to the model and loops until it answers without calling a tool.
func (a *Agent) Run(ctx context.Context, prompt string) (*Result, error) {
	maxSteps := a.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	maxTokens := a.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	tools := map[string]Tool{}
	var specs []any
	for _, t := range a.Tools {
		tools[t.Name] = t
		specs = append(specs, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Schema})
	}

	messages := []any{map[string]any{"role": "user", "content": prompt}}
	res := &Result{}
	for res.Steps < maxSteps {
		res.Steps++
		payload := map[string]any{"max_tokens": maxTokens, "messages": messages}
		if a.Model != "" {
			payload["model"] = a.Model
		}
		if a.System != "" {
			payload["system"] = a.System
		}
		if len(specs) > 0 {
			payload["tools"] = specs
			if res.Steps == maxSteps {
				payload["tool_choice"] = map[string]any{"type": "none"}
			}
		}
		resp, err := a.Client.Raw(ctx, payload)
		if err != nil {
			return res, err
		}
		res.InputTokens += resp.InputTokens
		res.OutputTokens += resp.OutputTokens
		res.CostUSD += resp.CostUSD
		messages = append(messages, map[string]any{"role": "assistant", "content": resp.Content})

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
				out, isErr := runTool(ctx, tools, b.Name, b.Input)
				results = append(results, map[string]any{
					"type": "tool_result", "tool_use_id": b.ID, "content": out, "is_error": isErr,
				})
			}
		}
		res.Answer = strings.TrimSpace(text.String())
		if len(results) == 0 {
			return res, nil
		}
		messages = append(messages, map[string]any{"role": "user", "content": results})
	}
	return res, nil
}

// runTool runs one tool call; failures (unknown tool, tool error) become the text the model reads.
func runTool(ctx context.Context, tools map[string]Tool, name string, input json.RawMessage) (out string, isErr bool) {
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
