// Package prompts holds the prompts of the research agents. Each prompt is a Markdown file in this directory, embedded in the
// binary, so that a prompt can be read and changed without touching Go code. A research agent's system prompt is made of parts: the
// base (Research), then Knowledge if it has the knowledge base, Spawn if it can spawn sub-agents, and, for a sub-agent, Subagent
// (see agent.ResearchAgent, agent.AddTeamTools and package agent_research).
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
	// WebSearch is the system prompt of the call the web_search tool makes.
	WebSearch = read("web_search.md")

	// KnowledgeReadOnly replaces Knowledge for an agent that may query the knowledge base but not add to it.
	KnowledgeReadOnly = read("knowledge_read_only.md")

	subagent    = template.Must(template.New("subagent.md").Parse(read("subagent.md")))
	searchPrefs = template.Must(template.New("search_prefs.md").Funcs(templateFuncs).Parse(read("search_prefs.md")))
	spawnRoles  = template.Must(template.New("spawn_roles.md").Parse(read("spawn_roles.md")))
)

var templateFuncs = template.FuncMap{"join": func(s []string) string { return strings.Join(s, ", ") }}

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

// SearchPrefs are the search preferences of an agent definition that go into its prompt (and into that of its web_search calls).
type SearchPrefs struct {
	Allowed, Blocked, Preferred []string
	// Recency is "any", "day", "week", "month" or "year".
	Recency      string
	Language     string
	Instructions string
}

// Empty reports whether there is nothing to say.
func (p SearchPrefs) Empty() bool {
	return len(p.Allowed) == 0 && len(p.Blocked) == 0 && len(p.Preferred) == 0 && (p.Recency == "" || p.Recency == "any") && p.Language == "" && p.Instructions == ""
}

// Search is the part of the prompt that tells an agent its search preferences; "" if there are none.
func Search(p SearchPrefs) string {
	if p.Empty() {
		return ""
	}
	recency := map[string]string{"day": "the last day", "week": "the last week", "month": "the last month", "year": "the last year"}
	var sb strings.Builder
	data := struct {
		SearchPrefs
		RecencyText string
	}{p, recency[p.Recency]}
	if err := searchPrefs.Execute(&sb, data); err != nil {
		panic("prompts: " + err.Error())
	}
	return sb.String()
}

// Role is a kind of sub-agent as the parent's prompt describes it.
type Role struct {
	Name, When, Does string
	// MaxInstances is the most of the role at once (0: no limit of its own).
	MaxInstances int
}

// RolesInfo is what the sub-agent part of the prompt of an agent with roles says.
type RolesInfo struct {
	// OnDemand are the roles the model starts itself; OnStart the sub-agents the runtime starts for it.
	OnDemand, OnStart []Role
	// MaxPerAgent is the most sub-agents it may have.
	MaxPerAgent int
}

// Roles is the part of the prompt of an agent whose definition has roles: instead of Spawn, which is the free-form policy.
func Roles(info RolesInfo) string {
	var sb strings.Builder
	if err := spawnRoles.Execute(&sb, info); err != nil {
		panic("prompts: " + err.Error())
	}
	return strings.TrimSpace(sb.String())
}

// Skill is a skill file of an agent definition.
type Skill struct{ Name, Content string }

// Skills is the part of the prompt with the skills, one section each; "" if there are none.
func Skills(skills []Skill) string {
	var parts []string
	for _, s := range skills {
		parts = append(parts, "## Skill: "+s.Name+"\n\n"+strings.TrimSpace(s.Content))
	}
	return Join(parts...)
}
