package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent/prompts"
)

// Names of the tools of a team of agents (see AddTeamTools).
const (
	ToolSpawnSubagent   = "spawn_subagent"
	ToolWaitSubagents   = "wait_subagents"
	ToolSendMessage     = "send_message"
	ToolReadMessages    = "read_messages"
	ToolListSiblings    = "list_siblings"
	maxMessageChars     = 10000
	maxReadWait         = 60 * time.Second
	maxSubagentSteps    = 40
	subagentNameChars   = 40
	subagentTaskChars   = 2000
	subagentReasonChars = 300
)

// Team is how an agent works with other agents: it starts sub-agents and exchanges messages with its siblings. The agent only
// sees the tools; the implementation (agent_research) decides what a sub-agent is, who the siblings are and how a message travels.
// One Team is the view of one agent.
type Team interface {
	// Spawn starts a sub-agent called name on the task, in the background, and returns at once with a text for the model. reason is
	// why the sub-agent has to exist: its siblings can read it (Siblings). maxSteps is the most model calls it may make (0: the
	// default). The name is unique among the sub-agents of the caller.
	Spawn(ctx context.Context, name, reason, task string, maxSteps int) (string, error)
	// Wait blocks until the named sub-agents (all of them if names is empty) have ended, and returns their reports.
	Wait(ctx context.Context, names []string) (string, error)
	// Send sends text to the sibling called to (a sub-agent of the same parent as the caller).
	Send(ctx context.Context, to, text string) (string, error)
	// Siblings lists the siblings of the caller (the other sub-agents of its parent) with the reason each was spawned for and
	// whether it still works, as a text for the model.
	Siblings(ctx context.Context) (string, error)
	// Receive returns the messages the caller has received and not read yet; if there are none and wait is positive, it waits up to
	// that long for one.
	Receive(ctx context.Context, wait time.Duration) (string, error)
}

// TeamTools says which tools of a team an agent gets.
type TeamTools struct {
	// Spawn gives it spawn_subagent and wait_subagents.
	Spawn bool
	// Messaging gives it send_message and read_messages (it has siblings).
	Messaging bool
}

// AddTeamTools gives a the tools of t that tools asks for, and the part of the prompt that says how to use them.
func AddTeamTools(a *Agent, t Team, tools TeamTools) {
	if tools.Spawn {
		a.System = prompts.Join(a.System, prompts.Spawn)
		a.Tools = append(a.Tools, spawnTool(t), waitTool(t))
	}
	if tools.Messaging {
		a.System = prompts.Join(a.System, prompts.Messaging)
		a.Tools = append(a.Tools, listSiblingsTool(t), sendTool(t), readTool(t))
	}
}

func spawnTool(t Team) Tool {
	return Tool{
		Name: ToolSpawnSubagent,
		Description: "Start a sub-agent on a sub-task, in the background. It researches like you and its final report is given to you by wait_subagents. " +
			"Returns at once, so you can start several sub-agents and go on. Each one costs as much as a full agent: spawn one only with a good reason.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "A short name for the sub-agent (letters, digits, - and _; at most 40), unique among your sub-agents. Siblings use it to message each other."},
				"reason":    map[string]any{"type": "string", "description": "Why this sub-agent has to exist: one sentence on what it covers that you cannot cover cheaply yourself (at most 300 characters). Its siblings can read it."},
				"task":      map[string]any{"type": "string", "description": "The sub-task: self-contained (the sub-agent knows nothing of your conversation), narrow, with what you want back."},
				"max_steps": map[string]any{"type": "integer", "description": "The most model calls the sub-agent makes (optional, default 10, at most 40)."},
			},
			"required": []string{"name", "reason", "task"},
		},
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var in struct {
				Name     string `json:"name"`
				Reason   string `json:"reason"`
				Task     string `json:"task"`
				MaxSteps int    `json:"max_steps"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			in.Name, in.Reason, in.Task = strings.TrimSpace(in.Name), strings.TrimSpace(in.Reason), strings.TrimSpace(in.Task)
			switch {
			case in.Name == "":
				return "", errors.New(`missing "name"`)
			case !ValidSubagentName(in.Name):
				return "", fmt.Errorf("name must be 1 to %d letters, digits, - or _", subagentNameChars)
			case in.Reason == "":
				return "", errors.New(`missing "reason": say why this sub-agent has to exist`)
			case len([]rune(in.Reason)) > subagentReasonChars:
				return "", fmt.Errorf("reason is too long (at most %d characters)", subagentReasonChars)
			case in.Task == "":
				return "", errors.New(`missing "task"`)
			case len([]rune(in.Task)) > subagentTaskChars:
				return "", fmt.Errorf("task is too long (at most %d characters)", subagentTaskChars)
			case in.MaxSteps < 0 || in.MaxSteps > maxSubagentSteps:
				return "", fmt.Errorf("max_steps must be between 1 and %d", maxSubagentSteps)
			}
			return t.Spawn(ctx, in.Name, in.Reason, in.Task, in.MaxSteps)
		},
	}
}

func waitTool(t Team) Tool {
	return Tool{
		Name:        ToolWaitSubagents,
		Description: "Wait until sub-agents you spawned have ended, and get their reports. Blocks. Without names it waits for all of your sub-agents.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The sub-agents to wait for (optional: all of them)."},
			},
		},
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var in struct {
				Names []string `json:"names"`
			}
			if len(input) > 0 {
				if err := json.Unmarshal(input, &in); err != nil {
					return "", fmt.Errorf("bad input: %w", err)
				}
			}
			return t.Wait(ctx, in.Names)
		},
	}
}

func sendTool(t Team) Tool {
	return Tool{
		Name: ToolSendMessage,
		Description: "Send a short message to a sibling sub-agent (one spawned by the same agent as you), by name. Returns at once; the sibling reads it with read_messages. " +
			"See list_siblings for who they are. You cannot message anyone else.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to":   map[string]any{"type": "string", "description": "The name of the sibling."},
				"text": map[string]any{"type": "string", "description": "The message (at most 10000 characters)."},
			},
			"required": []string{"to", "text"},
		},
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var in struct {
				To   string `json:"to"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			in.To, in.Text = strings.TrimSpace(in.To), strings.TrimSpace(in.Text)
			switch {
			case in.To == "":
				return "", errors.New(`missing "to"`)
			case in.Text == "":
				return "", errors.New(`missing "text"`)
			case len([]rune(in.Text)) > maxMessageChars:
				return "", fmt.Errorf("text is too long (at most %d characters)", maxMessageChars)
			}
			return t.Send(ctx, in.To, in.Text)
		},
	}
}

func listSiblingsTool(t Team) Tool {
	return Tool{
		Name: ToolListSiblings,
		Description: "List your siblings, the sub-agents you can message (spawned by the same agent as you): for each, its name, the reason it exists " +
			"and whether it is still working. Use it to decide whether someone can help you or needs something from you. The list changes as siblings are spawned and end.",
		Schema: map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			return t.Siblings(ctx)
		},
	}
}

func readTool(t Team) Tool {
	return Tool{
		Name:        ToolReadMessages,
		Description: "Read the messages your siblings sent you that you have not read yet. Returns at once unless wait_seconds is set: then, if there is no message, it waits up to that long for one.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"wait_seconds": map[string]any{"type": "integer", "description": "How long to wait for a message if there is none (optional, default 0, at most 60)."},
			},
		},
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var in struct {
				WaitSeconds int `json:"wait_seconds"`
			}
			if len(input) > 0 {
				if err := json.Unmarshal(input, &in); err != nil {
					return "", fmt.Errorf("bad input: %w", err)
				}
			}
			wait := time.Duration(max(in.WaitSeconds, 0)) * time.Second
			return t.Receive(ctx, min(wait, maxReadWait))
		},
	}
}

// ValidSubagentName reports whether s is 1 to subagentNameChars letters, digits, '-' or '_'.
func ValidSubagentName(s string) bool {
	if s == "" || len(s) > subagentNameChars {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
