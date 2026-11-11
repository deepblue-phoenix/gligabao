// Package prompts holds the prompts of the research agents. Each prompt is a Markdown file in this directory, embedded in the
// binary, so that a prompt can be read and changed without touching Go code. A research agent's system prompt is made of parts: the
// base (Research), then Knowledge if it has the knowledge base; a worker of the investigative loop gets Worker after them (see
// agent.ResearchAgent and package agent_research).
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
	// Worker is added to the prompt of a worker (a sub-agent of the investigative loop): how it works on one question and ends with
	// submit_findings.
	Worker = read("worker.md")
	// WebSearch is the system prompt of the call the web_search tool makes.
	WebSearch = read("web_search.md")

	// KnowledgeReadOnly replaces Knowledge for an agent that may query the knowledge base but not add to it.
	KnowledgeReadOnly = read("knowledge_read_only.md")

	searchPrefs = template.Must(template.New("search_prefs.md").Funcs(templateFuncs).Parse(read("search_prefs.md")))
)

var templateFuncs = template.FuncMap{"join": func(s []string) string { return strings.Join(s, ", ") }}

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
