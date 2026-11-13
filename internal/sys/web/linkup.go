package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// DefaultLinkupBaseURL is the API root of Linkup.
	DefaultLinkupBaseURL = "https://api.linkup.so"

	// linkupFetchCostUSD is the fee of one fetch call with mode "standard" and renderJs true.
	linkupFetchCostUSD = 0.005
	// maxLinkupBytes is how much of a Linkup response is read.
	maxLinkupBytes = 20 << 20
	// linkupSearchCostUSD is the fee of one search with depth "standard" and outputType "sourcedAnswer".
	linkupSearchCostUSD = 0.006
	// maxLinkupDomains is how many domains Linkup accepts in a list.
	maxLinkupDomains = 100
)

// linkupFetchBody is the JSON body of POST /v1/fetch (https://docs.linkup.so/pages/documentation/endpoints/fetch/overview.md).
type linkupFetchBody struct {
	URL      string `json:"url"`
	Mode     string `json:"mode"`
	RenderJS bool   `json:"renderJs"`
}

// linkupFetchResult is the part of the response that is used.
type linkupFetchResult struct {
	Markdown string `json:"markdown"`
}

// fetchLinkup reads a page with Linkup's /fetch: clean markdown of an HTML or PDF page, with JavaScript rendered (many pages are
// client-side rendered). Linkup does not authenticate to the page and does not follow links.
func fetchLinkup(ctx context.Context,
	d Deps,
	req FetchRequest) (*FetchResponse, error) {
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		return nil, errors.New("url must start with http:// or https://")
	}
	// Linkup follows redirects on its side, so only the requested host can be checked.
	target, err := http.NewRequestWithContext(ctx,
		http.MethodGet,
		req.URL,
		nil)
	if err != nil {
		return nil, err
	}
	if err := checkDomain(target.URL.Hostname(), req.AllowedDomains, req.BlockedDomains); err != nil {
		return nil, err
	}
	maxChars := req.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	payload, err := json.Marshal(linkupFetchBody{
		URL:      req.URL,
		Mode:     "standard",
		RenderJS: true,
	})
	if err != nil {
		return nil, err
	}
	body, err := linkupPost(ctx,
		d,
		"/v1/fetch",
		payload)
	if err != nil {
		return nil, fmt.Errorf("linkup fetch %s: %w",
			req.URL,
			err)
	}
	var out linkupFetchResult
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("linkup fetch %s: bad response: %w",
			req.URL,
			err)
	}
	return &FetchResponse{
		Provider: Linkup,
		URL:      req.URL,
		Text:     cutChars(strings.TrimSpace(out.Markdown), maxChars),
		CostUSD:  linkupFetchCostUSD,
	}, nil
}

// linkupPost sends the JSON payload to the Linkup endpoint path and returns the body of a 200 response.
func linkupPost(ctx context.Context,
	d Deps,
	path string,
	payload []byte) ([]byte, error) {
	key := d.LinkupKey
	if key == "" {
		return nil, errors.New("no API key (set LINKUP_API_KEY or linkup_api_key in the config)")
	}
	baseURL := strings.TrimRight(d.LinkupBaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultLinkupBaseURL
	}
	httpReq, err := http.NewRequestWithContext(ctx,
		http.MethodPost,
		baseURL+path,
		bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")
	client := d.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLinkupBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(body[:min(len(body), 300)])))
	}
	return body, nil
}

// cutChars keeps the first n characters (runes) of s.
func cutChars(s string,
	n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// linkupSearchBody is the JSON body of POST /v1/search (https://docs.linkup.so/pages/documentation/endpoints/search/overview.md).
type linkupSearchBody struct {
	Q              string   `json:"q"`
	Depth          string   `json:"depth"`
	OutputType     string   `json:"outputType"`
	IncludeDomains []string `json:"includeDomains,omitempty"`
	ExcludeDomains []string `json:"excludeDomains,omitempty"`
}

// linkupSearchResult is the "sourcedAnswer" response.
type linkupSearchResult struct {
	Answer  string `json:"answer"`
	Sources []struct {
		Name    string `json:"name"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
	} `json:"sources"`
}

// searchLinkup runs Linkup's /search with depth "standard" (one agentic iteration) and outputType "sourcedAnswer": a written answer with
// citations, and the sources with a snippet each. The domain lists map to includeDomains and excludeDomains (at most 100 each); the
// location is not used.
func searchLinkup(ctx context.Context,
	d Deps,
	req SearchRequest) (*SearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, errors.New("linkup search: empty query")
	}
	payload, err := json.Marshal(linkupSearchBody{
		Q:              req.Query,
		Depth:          "standard",
		OutputType:     "sourcedAnswer",
		IncludeDomains: req.AllowedDomains[:min(len(req.AllowedDomains), maxLinkupDomains)],
		ExcludeDomains: req.BlockedDomains[:min(len(req.BlockedDomains), maxLinkupDomains)],
	})
	if err != nil {
		return nil, err
	}
	body, err := linkupPost(ctx,
		d,
		"/v1/search",
		payload)
	if err != nil {
		return nil, fmt.Errorf("linkup search: %w", err)
	}
	var out linkupSearchResult
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("linkup search: bad response: %w", err)
	}
	results := make([]Result, len(out.Sources))
	for i, s := range out.Sources {
		results[i] = Result{
			URL:     s.URL,
			Title:   s.Name,
			Snippet: s.Snippet,
		}
	}
	return &SearchResponse{
		Provider: Linkup,
		Answer:   out.Answer,
		Results:  results,
		Queries:  []string{req.Query},
		Searches: 1,
		CostUSD:  linkupSearchCostUSD,
	}, nil
}
