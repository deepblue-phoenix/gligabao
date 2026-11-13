package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent/prompts"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/sys/web"
)

// Research runs a deep-research agent on the question and returns its report. The agent has two tools: web_search
// (Anthropic's web search) and fetch_url (no knowledge base: see ResearchAgent). The model must support the web search tool. Use ResearchAgent to change
// the model or the limits.
func Research(ctx context.Context,
	client *llm.Client,
	question string) (*Result, error) {
	if strings.TrimSpace(question) == "" {
		return nil, errors.New("agent: empty question")
	}
	httpClient := NewHTTPClient()
	defer httpClient.CloseIdleConnections()
	return ResearchAgent(client,
		httpClient,
		nil).Run(ctx, question)
}

// NewHTTPClient returns an HTTP client of its own for the page reads of one agent: its own transport (so its own connections and
// settings, shared with no other agent) and no cookie jar. Close its idle connections (CloseIdleConnections) when the agent is done.
func NewHTTPClient() *http.Client {
	var transport http.RoundTripper = http.DefaultTransport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = t.Clone()
	}
	return &http.Client{
		Transport: transport,
	}
}

// ResearchAgent returns the built-in default deep-research agent (agent_defs.Builtin), with up to 20 steps (model calls). httpClient
// reads the pages (fetch_url): give each agent its own (NewHTTPClient) for full isolation; nil makes one for this agent (without anyone
// to close it). kb is the knowledge base the agent works with: it gets the tools remember and query, and the part of the prompt that says
// how to use them; with nil (as in Research) it has only web_search and fetch_url. See FromDef for an agent of another definition.
func ResearchAgent(client *llm.Client,
	httpClient *http.Client,
	kb Knowledge) *Agent {
	return FromDef(agent_defs.Builtin(),
		client,
		httpClient,
		kb)
}

// fromDefOptions are what FromDef takes besides the definition; the defaults are Anthropic's search and the local fetch.
type fromDefOptions struct{ search, fetch web.Provider }

// FromDefOption changes how FromDef builds the agent.
type FromDefOption func(fromDefOptions) fromDefOptions

// WithWebProviders sets the providers of the web_search and the fetch_url tools; an empty one keeps the default.
func WithWebProviders(search,
	fetch web.Provider) FromDefOption {
	return func(o fromDefOptions) fromDefOptions {
		if search != "" {
			o.search = search
		}
		if fetch != "" {
			o.fetch = fetch
		}
		return o
	}
}

// FromDef builds the agent of a definition (package agent_defs): its model, limits, tools, search settings and system prompt, made of the
// base prompt of the definition, its search preferences and the part for the knowledge base. What is left to the caller is what depends on
// the place of the agent in a research: the skills
// (AddSkills, last). A tool with a limit of calls (Search.MaxCalls, Search.Fetch.MaxCalls) counts the calls of the Agent it is made for, so
// make a new Agent for each research. kb is as in ResearchAgent; a definition with knowledge access "none" ignores it.
func FromDef(def *agent_defs.Def,
	client *llm.Client,
	httpClient *http.Client,
	kb Knowledge,
	opts ...FromDefOption) *Agent {
	o := fromDefOptions{
		search: web.Anthropic,
		fetch:  web.Local,
	}
	for _, opt := range opts {
		o = opt(o)
	}
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
		Allowed:      def.Search.AllowedDomains,
		Blocked:      def.Search.BlockedDomains,
		Preferred:    def.Search.PreferredDomains,
		Recency:      def.Search.Recency,
		Language:     def.Search.Language,
		Instructions: def.Search.Instructions,
	}
	system := []string{def.SystemPrompt, prompts.Search(prefs)}
	if def.HasTool(agent_defs.ToolWebSearch) {
		a.Tools = append(a.Tools, webSearchTool(client, def.Search, o.search))
	}
	if def.HasTool(agent_defs.ToolFetchURL) {
		f := def.Search.Fetch
		a.Tools = append(a.Tools, fetchURLTool(httpClient, f, o.fetch))
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
			a.Tools = append(a.Tools,
				queryTool(kb),
				rememberTool(kb))
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
func AddSkills(a *Agent,
	skills []agent_defs.Skill) {
	parts := make([]prompts.Skill, len(skills))
	for i, s := range skills {
		parts[i] = prompts.Skill{
			Name:    s.Name,
			Content: s.Content,
		}
	}
	a.System = prompts.Join(a.System, prompts.Skills(parts))
}

// errToolLimit is what a tool says when the agent made as many calls as its definition allows.
func errToolLimit(tool string,
	limit int) error {
	return fmt.Errorf("%s limit reached (%d calls in this research): go on with what you have, and write your report",
		tool,
		limit)
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

func webSearchTool(client *llm.Client,
	cfg agent_defs.Search,
	provider web.Provider) Tool {
	counter := &callCounter{
		limit: cfg.MaxCalls,
	}
	system := prompts.Join(prompts.WebSearch, prompts.Search(prompts.SearchPrefs{
		Allowed:      cfg.AllowedDomains,
		Blocked:      cfg.BlockedDomains,
		Preferred:    cfg.PreferredDomains,
		Recency:      cfg.Recency,
		Language:     cfg.Language,
		Instructions: cfg.Instructions,
	}))
	var location *web.Location
	if l := cfg.Location; l != nil {
		location = &web.Location{
			Country:  l.Country,
			Region:   l.Region,
			City:     l.City,
			Timezone: l.Timezone,
		}
	}
	maxUses := cfg.MaxUsesPerCall
	if maxUses <= 0 {
		maxUses = agent_defs.DefaultSearchUses
	}
	return Tool{
		Name:        "web_search",
		Description: "Search the web. Returns a short summary of what was found, and the URLs of the sources.",
		Schema:      objectSchema("query", "What to search for."),
		Run: func(ctx context.Context,
			input json.RawMessage) (string, error) {
			query, err := stringArg(input, "query")
			if err != nil {
				return "", err
			}
			if !counter.allow() {
				return "", errToolLimit("web_search", counter.limit)
			}
			r, err := web.Search(ctx, provider, web.Deps{LLM: client}, web.SearchRequest{
				Query:          query,
				System:         system,
				MaxTokens:      1024,
				MaxUses:        maxUses,
				AllowedDomains: cfg.AllowedDomains,
				BlockedDomains: cfg.BlockedDomains,
				Location:       location,
			})
			if err != nil {
				return "", err
			}
			ReportUsage(ctx,
				r.InputTokens,
				r.OutputTokens,
				r.CostUSD)
			var sb strings.Builder
			sb.WriteString(r.Answer)
			sb.WriteString("\n\nSources:")
			for _, s := range r.Results {
				fmt.Fprintf(&sb,
					"\n- %s (%s)",
					s.URL,
					s.Title)
			}
			return sb.String(), nil
		},
	}
}

func fetchURLTool(httpClient *http.Client,
	cfg agent_defs.Fetch,
	provider web.Provider) Tool {
	counter := &callCounter{
		limit: cfg.MaxCalls,
	}
	maxChars := cfg.MaxPageChars
	if maxChars <= 0 {
		maxChars = agent_defs.DefaultMaxPageChars
	}
	return Tool{
		Name:        "fetch_url",
		Description: fmt.Sprintf("Download a web page and return its text (tags removed, cut to the first %d characters).", maxChars),
		Schema:      objectSchema("url", "The http(s) URL of the page."),
		Run: func(ctx context.Context,
			input json.RawMessage) (string, error) {
			url, err := stringArg(input, "url")
			if err != nil {
				return "", err
			}
			if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
				return "", errors.New("url must start with http:// or https://")
			}
			if !counter.allow() {
				return "", errToolLimit("fetch_url", counter.limit)
			}
			r, err := web.Fetch(ctx, provider, web.Deps{HTTP: httpClient}, web.FetchRequest{
				URL:            url,
				MaxChars:       maxChars,
				AllowedDomains: cfg.AllowedDomains,
				BlockedDomains: cfg.BlockedDomains,
			})
			if err != nil {
				return "", err
			}
			return r.Text, nil
		},
	}
}

// objectSchema is the schema of a tool whose input is {"<name>": "<string>"}.
func objectSchema(name,
	description string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			name: map[string]any{"type": "string", "description": description},
		},
		"required": []string{name},
	}
}

// stringArg reads the string field name of a tool input.
func stringArg(input json.RawMessage,
	name string) (string, error) {
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

// ToolFetchPage is the name fetch_url has in the investigative loop (loop = "investigative" in an agent definition): the same tool, the
// same result (the text of the page). The simple loop keeps the name fetch_url, and a definition lists fetch_url either way.
const ToolFetchPage = "fetch_page"

// FromDefInvestigative is FromDef for an agent that runs the investigative loop: the same tools and prompt, with fetch_url called
// fetch_page. The loop itself is in package agent_research/investigate; it takes the Tools and the System of the returned agent.
func FromDefInvestigative(def *agent_defs.Def,
	client *llm.Client,
	httpClient *http.Client,
	kb Knowledge,
	opts ...FromDefOption) *Agent {
	a := FromDef(def,
		client,
		httpClient,
		kb,
		opts...)
	for i, t := range a.Tools {
		if t.Name == agent_defs.ToolFetchURL {
			a.Tools[i].Name = ToolFetchPage
			a.Tools[i].Description = strings.ReplaceAll(t.Description,
				agent_defs.ToolFetchURL,
				ToolFetchPage)
		}
	}
	a.System = strings.ReplaceAll(a.System,
		agent_defs.ToolFetchURL,
		ToolFetchPage)
	return a
}
