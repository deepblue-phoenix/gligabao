// Package prompts holds the prompts of the research agents. Each prompt is a Markdown file in this directory, embedded in the
// binary, so that a prompt can be read and changed without touching Go code. A research agent's system prompt is made of parts: the
// base (Research), then Knowledge if it has the knowledge base, Spawn if it can spawn sub-agents, and, for a sub-agent, Subagent
// and Messaging (see agent.ResearchAgent, agent.AddTeamTools and package agent_research).
package prompts

import (
	"embed"
	"strings"
	"text/template"
)

//go:embed *.md
var files embed.FS

func read(name string) string {
	b, err := files.ReadFile(name)
	if err != nil {
		panic("prompts: " + err.Error()) // a missing embedded file is a build error
	}
	return strings.TrimSpace(string(b))
}

// The parts of the system prompt of a research agent, and the prompt of its web search call.
var (
	// Research is the base prompt of every research agent.
	Research = read("research.md")
	// Knowledge is added when the agent has the knowledge base (the tools query and remember).
	Knowledge = read("knowledge.md")
	// Spawn is added when the agent can spawn sub-agents: when spawning is warranted and how to do it.
	Spawn = read("spawn.md")
	// Messaging is added to a sub-agent: its siblings, and the tools to see and message them.
	Messaging = read("messaging.md")
	// WebSearch is the system prompt of the call the web_search tool makes.
	WebSearch = read("web_search.md")

	subagent = template.Must(template.New("subagent.md").Parse(read("subagent.md")))
)

// Subagent is the part of the prompt of a sub-agent: its name, the reason its parent gave for spawning it, and that its answer goes
// back to the parent.
func Subagent(name, reason string) string {
	var sb strings.Builder
	if err := subagent.Execute(&sb, struct{ Name, Reason string }{name, reason}); err != nil {
		panic("prompts: " + err.Error())
	}
	return sb.String()
}

// Join puts prompt parts together, one blank line between them; empty parts are skipped.
func Join(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}
