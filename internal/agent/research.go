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

	"github.com/deepblue-phoenix/gligabao/internal/ai"
)

const (
	researchSystem = `You are a deep-research assistant. Answer the user's question by researching the web.
Search several times with different queries, read the most relevant pages with fetch_url, and check claims against
more than one source. Stop when you can answer well. Then write a report: a short summary first, then the details,
and a list of the source URLs you used. State what you could not find out; never guess.`

	// researchKnowledgeSystem is added to the prompt when the agent has a knowledge base (the tools remember and query).
	researchKnowledgeSystem = `

You also work with an internal knowledge base, through two tools. Use query, besides web_search and fetch_url, to check what
the knowledge base knows already before you search the web for it, and to check what you found against it. While you search and
read, call remember on the important parts (facts, figures, dates, quotes, each with its context, as short self-contained snippets;
never whole pages) and give the URL as its source. Remember calls do not block by default (sync=false): the text is queued and you
go on. Use sync=true only when you need the text absorbed before you go on, for example to query for it right away. Text that is
still queued may not be found by query yet.`

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

// ResearchAgent returns the deep-research agent, with up to 20 steps (model calls). httpClient reads the pages (fetch_url): give each
// agent its own (NewHTTPClient) for full isolation; nil makes one for this agent (without anyone to close it). kb is the knowledge
// base the agent works with: it gets the tools remember and query, and the part of the prompt that says how to use them; with nil
// (as in Research) it has only web_search and fetch_url.
func ResearchAgent(client *ai.Client, httpClient *http.Client, kb Knowledge) *Agent {
	if httpClient == nil {
		httpClient = NewHTTPClient()
	}
	a := &Agent{
		Client:    client,
		System:    researchSystem,
		Tools:     []Tool{webSearchTool(client), fetchURLTool(httpClient)},
		MaxTokens: 8192,
		MaxSteps:  20,
	}
	if kb != nil {
		a.System += researchKnowledgeSystem
		a.Tools = append(a.Tools, queryTool(kb), rememberTool(kb))
	}
	return a
}

func webSearchTool(client *ai.Client) Tool {
	return Tool{
		Name:        "web_search",
		Description: "Search the web. Returns a short summary of what was found, and the URLs of the sources.",
		Schema:      objectSchema("query", "What to search for."),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			query, err := stringArg(input, "query")
			if err != nil {
				return "", err
			}
			r, err := client.WebSearch(ctx, ai.WebSearchRequest{
				System:    "Search the web for the query. Report what you found in at most 300 words, with the facts and dates.",
				Prompt:    query,
				MaxTokens: 1024,
				MaxUses:   2,
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

func fetchURLTool(httpClient *http.Client) Tool {
	return Tool{
		Name:        "fetch_url",
		Description: "Download a web page and return its text (tags removed, cut to the first 15000 characters).",
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
			resp, err := httpClient.Do(req)
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
			return pageText(string(body)), nil
		},
	}
}

var (
	reInvisible = regexp.MustCompile(`(?is)<(script|style|noscript)\b.*?</(script|style|noscript)>`)
	reTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace     = regexp.MustCompile(`\s+`)
)

// pageText turns HTML into plain text, cut to maxPageChars characters. It is crude on purpose.
func pageText(html string) string {
	s := reInvisible.ReplaceAllString(html, " ")
	s = reTag.ReplaceAllString(s, " ")
	s = strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > maxPageChars {
		s = string(r[:maxPageChars])
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
