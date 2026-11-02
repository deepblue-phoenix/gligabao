package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/deepblue-phoenix/gligabao/internal/agent/prompts"
	"github.com/deepblue-phoenix/gligabao/internal/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
)

const (
	// maxPageChars is how much of a page the model gets.
	maxPageChars = 15000
	// maxPageBytes is how much of a page is downloaded.
	maxPageBytes = 1 << 20
)

// Research runs a deep-research agent on the question and returns its report. The agent has two tools: web_search
// (Anthropic's web search) and fetch_url (no knowledge base: see ResearchAgent). The model must support the web search tool. Use ResearchAgent to change
// the model or the limits.
func Research(ctx context.Context, client *ai.Client, question string) (*Result, error) {
	if strings.TrimSpace(question) == "" {
		return nil, errors.New("agent: empty question")
	}
	httpClient := NewHTTPClient()
	defer httpClient.CloseIdleConnections()
	return ResearchAgent(client, httpClient, nil).Run(ctx, question)
}

// NewHTTPClient returns an HTTP client of its own for the page reads of one agent: its own transport (so its own connections and
// settings, shared with no other agent) and no cookie jar. Close its idle connections (CloseIdleConnections) when the agent is done.
func NewHTTPClient() *http.Client {
	var transport http.RoundTripper = http.DefaultTransport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = t.Clone()
	}
	return &http.Client{Transport: transport}
}

// ResearchAgent returns the built-in default deep-research agent (agent_defs.Builtin), with up to 20 steps (model calls). httpClient
// reads the pages (fetch_url): give each agent its own (NewHTTPClient) for full isolation; nil makes one for this agent (without anyone
// to close it). kb is the knowledge base the agent works with: it gets the tools remember and query, and the part of the prompt that says
// how to use them; with nil (as in Research) it has only web_search and fetch_url. See FromDef for an agent of another definition.
func ResearchAgent(client *ai.Client, httpClient *http.Client, kb Knowledge) *Agent {
	return FromDef(agent_defs.Builtin(), client, httpClient, kb)
}

// FromDef builds the agent of a definition (package agent_defs): its model, limits, tools, search settings and system prompt, made of the
// base prompt of the definition, its search preferences and the part for the knowledge base. What is left to the caller is what depends on
// the place of the agent in a research: the parts for sub-agents and siblings (AddTeamTools), a sub-agent's own preamble, and the skills
// (AddSkills, last). A tool with a limit of calls (Search.MaxCalls, Search.Fetch.MaxCalls) counts the calls of the Agent it is made for, so
// make a new Agent for each research. kb is as in ResearchAgent; a definition with knowledge access "none" ignores it.
func FromDef(def *agent_defs.Def, client *ai.Client, httpClient *http.Client, kb Knowledge) *Agent {
	if httpClient == nil {
		httpClient = NewHTTPClient()
	}
	a := &Agent{
		Client:    client,
		Name:      trajectoryName(def),
		Version:   def.Version,
		Model:     def.Model,
		MaxTokens: def.MaxTokens,
		MaxSteps:  def.MaxSteps,
	}
	prefs := prompts.SearchPrefs{
		Allowed: def.Search.AllowedDomains, Blocked: def.Search.BlockedDomains, Preferred: def.Search.PreferredDomains,
		Recency: def.Search.Recency, Language: def.Search.Language, Instructions: def.Search.Instructions,
	}
	system := []string{def.SystemPrompt, prompts.Search(prefs)}
	if def.HasTool(agent_defs.ToolWebSearch) {
		a.Tools = append(a.Tools, webSearchTool(client, def.Search))
	}
	if def.HasTool(agent_defs.ToolFetchURL) {
		f := def.Search.Fetch
		a.Tools = append(a.Tools, fetchURLTool(httpClient, f))
		// The domains of fetch_url are not those of web_search: say them too.
		if len(f.AllowedDomains) > 0 {
			system = append(system, "fetch_url reads pages only of these sites: "+strings.Join(f.AllowedDomains, ", ")+".")
		}
		if len(f.BlockedDomains) > 0 {
			system = append(system, "fetch_url refuses pages of these sites: "+strings.Join(f.BlockedDomains, ", ")+".")
		}
	}
	if kb != nil && def.Knowledge.Access != agent_defs.AccessNone {
		k := def.Knowledge
		if k.Access == agent_defs.AccessReadOnly {
			system = append(system, prompts.KnowledgeReadOnly)
			a.Tools = append(a.Tools, queryTool(kb))
		} else {
			system = append(system, prompts.Knowledge)
			a.Tools = append(a.Tools, queryTool(kb), rememberTool(kb))
		}
		if k.QueryFirst {
			system = append(system, "Always start by asking the knowledge base what it knows about the topic, before you search the web.")
		}
		if k.Instructions != "" {
			system = append(system, k.Instructions)
		}
	}
	a.System = prompts.Join(system...)
	return a
}

// trajectoryName is the name of the agent of a definition in its ATIF trajectory: "gligabao-research" for the default agent, as it always
// was, and gligabao-<name> for the others.
func trajectoryName(def *agent_defs.Def) string {
	if def.Name == agent_defs.DefaultName {
		return "gligabao-research"
	}
	return "gligabao-" + def.Name
}

// AddSkills appends the skills to the system prompt of a: the last part of it.
func AddSkills(a *Agent, skills []agent_defs.Skill) {
	parts := make([]prompts.Skill, len(skills))
	for i, s := range skills {
		parts[i] = prompts.Skill{Name: s.Name, Content: s.Content}
	}
	a.System = prompts.Join(a.System, prompts.Skills(parts))
}

// errToolLimit is what a tool says when the agent made as many calls as its definition allows.
func errToolLimit(tool string, limit int) error {
	return fmt.Errorf("%s limit reached (%d calls in this research): go on with what you have, and write your report", tool, limit)
}

// callCounter counts the calls of a tool; with a limit of 0 it never refuses.
type callCounter struct {
	n     atomic.Int64
	limit int
}

func (c *callCounter) allow() bool {
	if c.limit <= 0 {
		return true
	}
	return c.n.Add(1) <= int64(c.limit)
}

func webSearchTool(client *ai.Client, cfg agent_defs.Search) Tool {
	counter := &callCounter{limit: cfg.MaxCalls}
	system := prompts.Join(prompts.WebSearch, prompts.Search(prompts.SearchPrefs{
		Allowed: cfg.AllowedDomains, Blocked: cfg.BlockedDomains, Preferred: cfg.PreferredDomains,
		Recency: cfg.Recency, Language: cfg.Language, Instructions: cfg.Instructions,
	}))
	var location *ai.WebSearchLocation
	if l := cfg.Location; l != nil {
		location = &ai.WebSearchLocation{Country: l.Country, Region: l.Region, City: l.City, Timezone: l.Timezone}
	}
	maxUses := cfg.MaxUsesPerCall
	if maxUses <= 0 {
		maxUses = agent_defs.DefaultSearchUses
	}
	return Tool{
		Name:        "web_search",
		Description: "Search the web. Returns a short summary of what was found, and the URLs of the sources.",
		Schema:      objectSchema("query", "What to search for."),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			query, err := stringArg(input, "query")
			if err != nil {
				return "", err
			}
			if !counter.allow() {
				return "", errToolLimit("web_search", counter.limit)
			}
			r, err := client.WebSearch(ctx, ai.WebSearchRequest{
				System:         system,
				Prompt:         query,
				MaxTokens:      1024,
				MaxUses:        maxUses,
				AllowedDomains: cfg.AllowedDomains,
				BlockedDomains: cfg.BlockedDomains,
				Location:       location,
			})
			if err != nil {
				return "", err
			}
			ReportUsage(ctx, r.InputTokens, r.OutputTokens, r.CostUSD)
			var sb strings.Builder
			sb.WriteString(r.Text)
			sb.WriteString("\n\nSources:")
			for _, s := range r.Sources {
				fmt.Fprintf(&sb, "\n- %s (%s)", s.URL, s.Title)
			}
			return sb.String(), nil
		},
	}
}

// hostMatches reports whether host is one of the listed hosts or a subdomain of one.
func hostMatches(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, h := range list {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// checkDomain refuses a host that the allowed and blocked lists of fetch_url do not let through.
func checkDomain(host string, cfg agent_defs.Fetch) error {
	switch {
	case hostMatches(host, cfg.BlockedDomains):
		return fmt.Errorf("%s is blocked: you may not read pages of that site", host)
	case len(cfg.AllowedDomains) > 0 && !hostMatches(host, cfg.AllowedDomains):
		return fmt.Errorf("%s is not allowed: you may read pages only of %s", host, strings.Join(cfg.AllowedDomains, ", "))
	}
	return nil
}

func fetchURLTool(httpClient *http.Client, cfg agent_defs.Fetch) Tool {
	counter := &callCounter{limit: cfg.MaxCalls}
	maxChars := cfg.MaxPageChars
	if maxChars <= 0 {
		maxChars = agent_defs.DefaultMaxPageChars
	}
	// A redirect may not leave the allowed sites. The client is the agent's own; a copy carries the check.
	client := *httpClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return checkDomain(req.URL.Hostname(), cfg)
	}
	return Tool{
		Name:        "fetch_url",
		Description: fmt.Sprintf("Download a web page and return its text (tags removed, cut to the first %d characters).", maxChars),
		Schema:      objectSchema("url", "The http(s) URL of the page."),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			url, err := stringArg(input, "url")
			if err != nil {
				return "", err
			}
			if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
				return "", errors.New("url must start with http:// or https://")
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return "", err
			}
			if err := checkDomain(req.URL.Hostname(), cfg); err != nil {
				return "", err
			}
			if !counter.allow() {
				return "", errToolLimit("fetch_url", counter.limit)
			}
			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("%s: status %d", url, resp.StatusCode)
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
			if err != nil {
				return "", err
			}
			return pageTextMax(string(body), maxChars), nil
		},
	}
}

var (
	reInvisible = regexp.MustCompile(`(?is)<(script|style|noscript)\b.*?</(script|style|noscript)>`)
	reTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace     = regexp.MustCompile(`\s+`)
)

// pageText turns HTML into plain text, cut to maxPageChars characters. It is crude on purpose.
func pageText(html string) string { return pageTextMax(html, maxPageChars) }

// pageTextMax is pageText cut to max characters.
func pageTextMax(html string, max int) string {
	s := reInvisible.ReplaceAllString(html, " ")
	s = reTag.ReplaceAllString(s, " ")
	s = strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// objectSchema is the schema of a tool whose input is {"<name>": "<string>"}.
func objectSchema(name, description string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{name: map[string]any{"type": "string", "description": description}},
		"required":   []string{name},
	}
}

// stringArg reads the string field name of a tool input.
func stringArg(input json.RawMessage, name string) (string, error) {
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	s, _ := m[name].(string)
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("missing %q", name)
	}
	return s, nil
}
