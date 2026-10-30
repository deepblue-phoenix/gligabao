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

	// maxPageChars is how much of a page the model gets.
	maxPageChars = 15000
	// maxPageBytes is how much of a page is downloaded.
	maxPageBytes = 1 << 20
)

// Research runs a deep-research agent on the question and returns its report. The agent has two tools: web_search
// (Anthropic's web search) and fetch_url. The model must support the web search tool. Use ResearchAgent to change
// the model or the limits.
func Research(ctx context.Context, client *ai.Client, question string) (*Result, error) {
	if strings.TrimSpace(question) == "" {
		return nil, errors.New("agent: empty question")
	}
	return ResearchAgent(client).Run(ctx, question)
}

// ResearchAgent returns the deep-research agent, with up to 20 steps (model calls).
func ResearchAgent(client *ai.Client) *Agent {
	return &Agent{
		Client:    client,
		System:    researchSystem,
		Tools:     []Tool{webSearchTool(client), fetchURLTool(http.DefaultClient)},
		MaxTokens: 8192,
		MaxSteps:  20,
	}
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
