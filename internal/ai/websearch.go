package ai

// Web search with Anthropic's cloud-based web search tool: a "server tool", so Anthropic runs the searches and reads
// the pages; gligabao only sends the request and gets back the model's answer (with citations) in the same response.
// It needs nothing but the Anthropic API key the client already has. See
// https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/web-search-tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// webSearchToolType is the version of the web search tool that is requested.
	webSearchToolType = "web_search_20250305"
	// WebSearchRequestUSD is what Anthropic charges for each search the tool makes ($10 per 1,000), on top of the
	// tokens. The price table has per-token prices only, so it is added to the cost of a web search call here.
	WebSearchRequestUSD = 0.01
	// defaultWebSearchUses is the most searches the model may make for one request when the caller sets none.
	defaultWebSearchUses = 3
	// maxWebSearchContinuations is how often a paused turn is resumed (see WebSearch).
	maxWebSearchContinuations = 3
)

// WebSearchRequest asks the model to research something with the web search tool.
type WebSearchRequest struct {
	// System is the system prompt; Prompt is the user turn, naming what to search for.
	System string
	Prompt string
	// Model is the model that runs the searches and writes the answer; empty uses the client's default model. It must
	// be one that supports the web search tool.
	Model string
	// MaxTokens bounds the answer; 0 uses the client's default.
	MaxTokens int
	// MaxUses is the most searches the model may make; 0 means 3.
	MaxUses int
}

// WebSource is a web page that backs the answer of a WebSearch.
type WebSource struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	// PageAge is how old the page is, as the search engine says ("April 30, 2025"); empty if unknown.
	PageAge string `json:"page_age,omitempty"`
}

// WebSearchResponse is the result of a WebSearch.
type WebSearchResponse struct {
	// Text is the model's answer, written from what the searches found.
	Text string
	// Queries are the queries the model sent to the search engine, in order (it decides them, from the prompt).
	Queries []string
	// Sources are the pages the answer cites, once each; if it cites none, the pages the searches returned.
	Sources []WebSource
	// SearchErrors are the error codes of searches that failed ("max_uses_exceeded", "unavailable", ...); the model
	// goes on without them, so they are not a failure of the call.
	SearchErrors []string
	// Searches is the number of searches that were made and billed.
	Searches   int
	Provider   string
	Model      string
	StopReason string
	// InputTokens includes cache tokens; OutputTokens is the generated tokens.
	InputTokens  int
	OutputTokens int
	// CostUSD is the tokens' cost (0 if the model's price is unknown) plus WebSearchRequestUSD for each search.
	CostUSD float64
}

// WebSearch runs the model with the web search tool and returns its answer, its sources and what it cost. The model
// decides which searches to make (at most req.MaxUses). A turn that Anthropic pauses because it ran long (stop reason
// "pause_turn") is resumed a few times; the usage of every round is summed.
func (c *Client) WebSearch(ctx context.Context, req WebSearchRequest) (*WebSearchResponse, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("ai: web search: empty prompt")
	}
	model := req.Model
	if model == "" {
		model = c.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.maxTokens
	}
	maxUses := req.MaxUses
	if maxUses <= 0 {
		maxUses = defaultWebSearchUses
	}

	// The messages are kept raw: to resume a paused turn the model's own content blocks (searches, results,
	// encrypted contents) go back to it unchanged.
	messages := []any{map[string]any{"role": "user", "content": req.Prompt}}
	out := &WebSearchResponse{Provider: ProviderAnthropic}
	var usage Usage
	var text strings.Builder
	cited := map[string]bool{}
	var retrieved []WebSource

	for round := 0; ; round++ {
		payload := map[string]any{
			"model":      model,
			"max_tokens": maxTokens,
			"messages":   messages,
			"tools":      []any{map[string]any{"type": webSearchToolType, "name": "web_search", "max_uses": maxUses}},
		}
		if req.System != "" {
			payload["system"] = req.System
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("ai: marshal web search request: %w", err)
		}
		data, err := c.post(ctx, body)
		if err != nil {
			return nil, err
		}
		var r webSearchAPIResponse
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("ai: decode web search response: %w", err)
		}

		out.Model, out.StopReason = r.Model, r.StopReason
		usage.InputTokens += r.Usage.InputTokens
		usage.OutputTokens += r.Usage.OutputTokens
		usage.CacheWriteTokens += r.Usage.CacheCreationInputTokens
		usage.CacheReadTokens += r.Usage.CacheReadInputTokens
		out.Searches += r.Usage.ServerToolUse.WebSearchRequests

		for _, raw := range r.Content {
			var b webSearchBlock
			if err := json.Unmarshal(raw, &b); err != nil {
				return nil, fmt.Errorf("ai: decode web search content block: %w", err)
			}
			switch b.Type {
			case "text":
				text.WriteString(b.Text)
				for _, ci := range b.Citations {
					if ci.URL != "" && !cited[ci.URL] {
						cited[ci.URL] = true
						out.Sources = append(out.Sources, WebSource{URL: ci.URL, Title: ci.Title})
					}
				}
			case "server_tool_use":
				if b.Name == "web_search" && b.Input.Query != "" {
					out.Queries = append(out.Queries, b.Input.Query)
				}
			case "web_search_tool_result":
				results, errCode := parseWebSearchResults(b.Content)
				if errCode != "" {
					out.SearchErrors = append(out.SearchErrors, errCode)
				}
				retrieved = append(retrieved, results...)
			}
		}

		if r.StopReason != "pause_turn" || round >= maxWebSearchContinuations {
			break
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": r.Content})
	}

	// The retrieved pages carry the page age and the title the citations lack; a cited page takes them from there.
	if len(out.Sources) == 0 {
		out.Sources = dedupeSources(retrieved)
	} else {
		byURL := map[string]WebSource{}
		for _, s := range retrieved {
			byURL[s.URL] = s
		}
		for i, s := range out.Sources {
			if r, ok := byURL[s.URL]; ok {
				if s.Title == "" {
					out.Sources[i].Title = r.Title
				}
				out.Sources[i].PageAge = r.PageAge
			}
		}
	}

	out.Text = strings.TrimSpace(text.String())
	out.InputTokens = usage.InputTokens + usage.CacheWriteTokens + usage.CacheReadTokens
	out.OutputTokens = usage.OutputTokens
	cost, _ := c.prices.Cost(out.Model, usage)
	out.CostUSD = cost + float64(out.Searches)*WebSearchRequestUSD
	return out, nil
}

// parseWebSearchResults reads the content of a web_search_tool_result block: the list of pages found, or an error
// object ({"type":"web_search_tool_result_error","error_code":"..."}), whose code is returned.
func parseWebSearchResults(raw json.RawMessage) (pages []WebSource, errCode string) {
	var list []struct {
		Type    string `json:"type"`
		URL     string `json:"url"`
		Title   string `json:"title"`
		PageAge string `json:"page_age"`
	}
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, r := range list {
			if r.Type == "web_search_result" && r.URL != "" {
				pages = append(pages, WebSource{URL: r.URL, Title: r.Title, PageAge: r.PageAge})
			}
		}
		return pages, ""
	}
	var e struct {
		ErrorCode string `json:"error_code"`
	}
	if json.Unmarshal(raw, &e) == nil && e.ErrorCode != "" {
		return nil, e.ErrorCode
	}
	return nil, "unknown"
}

func dedupeSources(in []WebSource) []WebSource {
	seen := map[string]bool{}
	var out []WebSource
	for _, s := range in {
		if !seen[s.URL] {
			seen[s.URL] = true
			out = append(out, s)
		}
	}
	return out
}

//---------------------------------------------------------------------------------------------
// Wire types.

type webSearchAPIResponse struct {
	Model      string            `json:"model"`
	StopReason string            `json:"stop_reason"`
	Content    []json.RawMessage `json:"content"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		ServerToolUse            struct {
			WebSearchRequests int `json:"web_search_requests"`
		} `json:"server_tool_use"`
	} `json:"usage"`
}

// webSearchBlock is the part of a content block that WebSearch reads, whatever its type (text, server_tool_use,
// web_search_tool_result).
type webSearchBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Name  string `json:"name"`
	Input struct {
		Query string `json:"query"`
	} `json:"input"`
	Content   json.RawMessage `json:"content"`
	Citations []struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"citations"`
}
