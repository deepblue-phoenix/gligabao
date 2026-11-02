package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/agent/prompts"
)

// Names of the tools of a team of agents (see AddTeamTools).
const (
	ToolSpawnSubagent   = "spawn_subagent"
	ToolWaitSubagents   = "wait_subagents"
	maxSubagentSteps    = 40
	subagentNameChars   = 40
	subagentTaskChars   = 2000
	subagentReasonChars = 300
)

// Team is how an agent works with other agents: it starts sub-agents and waits for them. The agent only
// sees the tools; the implementation (agent_research) decides what a sub-agent is.
// One Team is the view of one agent.
type Team interface {
	// Spawn starts a sub-agent called name on the task, in the background, and returns at once with a text for the model. role is the
	// kind of sub-agent, one of the roles of the caller's definition ("" when the caller spawns free-form, without roles). reason is
	// why the sub-agent has to exist. maxSteps is the most model calls it may make (0: the
	// default). The name is unique among the sub-agents of the caller.
	Spawn(ctx context.Context, role, name, reason, task string, maxSteps int) (string, error)
	// Wait blocks until the named sub-agents (all of them if names is empty) have ended, and returns their reports.
	Wait(ctx context.Context, names []string) (string, error)
}

// Role is a kind of sub-agent that an agent may start with spawn_subagent (a role of its definition): what the model is told of it.
type Role struct {
	Name, Does, When string
}

// TeamTools says which tools of a team an agent gets.
type TeamTools struct {
	// Spawn gives it spawn_subagent and wait_subagents.
	Spawn bool
	// Wait gives it wait_subagents alone: it has sub-agents that were started for it, and starts none itself.
	Wait bool
	// Roles, if there are any, make spawn_subagent take a "role" argument, one of these, which is required.
	Roles []Role
	// Prompt is the part of the system prompt about the sub-agents, in place of the free-form one (prompts.Spawn); it is added when
	// Spawn or Wait is set. Empty: prompts.Spawn with Spawn, nothing with Wait alone.
	Prompt string
}

// AddTeamTools gives a the tools of t that tools asks for, and the part of the prompt that says how to use them.
func AddTeamTools(a *Agent, t Team, tools TeamTools) {
	if tools.Spawn || tools.Wait {
		prompt := tools.Prompt
		if prompt == "" && tools.Spawn {
			prompt = prompts.Spawn
		}
		a.System = prompts.Join(a.System, prompt)
	}
	if tools.Spawn {
		a.Tools = append(a.Tools, spawnTool(t, tools.Roles))
	}
	if tools.Spawn || tools.Wait {
		a.Tools = append(a.Tools, waitTool(t))
	}
}

func spawnTool(t Team, roles []Role) Tool {
	desc := "Start a sub-agent on a sub-task, in the background. It researches like you and its final report is given to you by wait_subagents. " +
		"Returns at once, so you can start several sub-agents and go on. Each one costs as much as a full agent: spawn one only with a good reason."
	props := map[string]any{
		"name":      map[string]any{"type": "string", "description": "A short name for the sub-agent (letters, digits, - and _; at most 40), unique among your sub-agents."},
		"reason":    map[string]any{"type": "string", "description": "Why this sub-agent has to exist: one sentence on what it covers that you cannot cover cheaply yourself (at most 300 characters)."},
		"task":      map[string]any{"type": "string", "description": "The sub-task: self-contained (the sub-agent knows nothing of your conversation), narrow, with what you want back."},
		"max_steps": map[string]any{"type": "integer", "description": "The most model calls the sub-agent makes (optional, at most 40; the role has a default and its own limit when you have roles, else the default is 10)."},
	}
	required := []string{"name", "reason", "task"}
	roleNames := map[string]bool{}
	if len(roles) > 0 {
		var names []string
		var lines []string
		for _, r := range roles {
			names = append(names, r.Name)
			roleNames[r.Name] = true
			lines = append(lines, fmt.Sprintf("%s: %s", r.Name, r.Does))
		}
		props["role"] = map[string]any{"type": "string", "enum": names, "description": "The kind of sub-agent: " + strings.Join(lines, "; ")}
		required = append([]string{"role"}, required...)
		desc += " Choose the role of the sub-agent: it decides what the sub-agent is and how it works."
	}
	return Tool{
		Name:        ToolSpawnSubagent,
		Description: desc,
		Schema:      map[string]any{"type": "object", "properties": props, "required": required},
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var in struct {
				Role     string `json:"role"`
				Name     string `json:"name"`
				Reason   string `json:"reason"`
				Task     string `json:"task"`
				MaxSteps int    `json:"max_steps"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("bad input: %w", err)
			}
			in.Role, in.Name, in.Reason, in.Task = strings.TrimSpace(in.Role), strings.TrimSpace(in.Name), strings.TrimSpace(in.Reason), strings.TrimSpace(in.Task)
			switch {
			case len(roles) > 0 && in.Role == "":
				return "", errors.New(`missing "role"`)
			case len(roles) > 0 && !roleNames[in.Role]:
				return "", fmt.Errorf("unknown role %q (yours: %s)", in.Role, roleList(roles))
			case len(roles) == 0 && in.Role != "":
				return "", errors.New("you have no roles: leave out \"role\"")
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
			return t.Spawn(ctx, in.Role, in.Name, in.Reason, in.Task, in.MaxSteps)
		},
	}
}

func roleList(roles []Role) string {
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
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
