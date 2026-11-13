package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const (
	// DefaultKeenableBaseURL is the API root of Keenable.
	DefaultKeenableBaseURL = "https://api.keenable.ai"

	// keenableTitle names gligabao in the X-Keenable-Title header, which the keyless (/public) endpoint requires.
	keenableTitle = "gligabao"
	// keenableSnippetMaxLength caps the text of each result (the API takes 180 to 10000 characters).
	keenableSnippetMaxLength = 2000
	// keenableCreditUSD is the list price of one credit: 1,000 requests cost 4 US dollars once the free monthly allowance is used up.
	keenableCreditUSD = 0.004
	// maxKeenableDomains is how many hosts the exclude_domains list of the API takes.
	maxKeenableDomains = 1000
	// maxKeenableBytes is how much of a Keenable response is read.
	maxKeenableBytes = 20 << 20
)

// keenableSearchBody is the JSON body of POST /v1/search (https://docs.keenable.ai/api-reference/search.md). Mode is left to the API
// default (pro); the result count is left to its default (10).
type keenableSearchBody struct {
	Query            string   `json:"query"`
	Site             string   `json:"site,omitempty"`
	ExcludeDomains   []string `json:"exclude_domains,omitempty"`
	SnippetMaxLength int      `json:"snippet_max_length"`
	// DisableCards replaces the cards (price, exchange rate or company facts pages Keenable writes itself) with ordinary pages: a card's
	// URL stops working after a day, so it cannot be cited later.
	DisableCards bool `json:"disable_cards"`
}

// keenableSearchResponse is the part of the 200 response that is used.
type keenableSearchResponse struct {
	Results []keenableHit `json:"results"`
	// Usage is present on calls made with a key; the keyless endpoint leaves it out.
	Usage *keenableUsage `json:"usage"`
}

// keenableHit is one result. Snippet is the text of the page; Description is deprecated and always empty.
type keenableHit struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet"`
	PublishedAt string `json:"published_at"`
}

// keenableUsage is what a keyed call was billed. Paid is false when the call came from the free monthly allowance.
type keenableUsage struct {
	Credits float64 `json:"credits"`
	Paid    bool    `json:"paid"`
}

// searchKeenable runs Keenable's search and returns the ranked results with their text (no written answer). Without a key the call
// goes to the keyless /v1/search/public endpoint, which is rate limited per IP address; with one, to /v1/search.
//
// AllowedDomains become the site parameter (one domain) or site: operators of the query (several); BlockedDomains become
// exclude_domains. The location is not used.
func searchKeenable(ctx context.Context,
	d Deps,
	req SearchRequest) (*SearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, errors.New("keenable search: empty query")
	}
	if len(req.BlockedDomains) > maxKeenableDomains {
		return nil, fmt.Errorf("keenable search: at most %d blocked domains, got %d",
			maxKeenableDomains,
			len(req.BlockedDomains))
	}
	site, query, err := keenableScope(req.Query,
		req.AllowedDomains)
	if err != nil {
		return nil, fmt.Errorf("keenable search: %w", err)
	}
	payload, err := json.Marshal(keenableSearchBody{
		Query:            query,
		Site:             site,
		ExcludeDomains:   req.BlockedDomains,
		SnippetMaxLength: keenableSnippetMaxLength,
		DisableCards:     true,
	})
	if err != nil {
		return nil, err
	}
	path, header := keenableEndpoint(d.KeenableKey,
		"search")
	body, err := keenableSend(ctx,
		d,
		http.MethodPost,
		path,
		header,
		payload)
	if err != nil {
		return nil, fmt.Errorf("keenable search: %w", err)
	}
	var out keenableSearchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("keenable search: bad response: %w", err)
	}
	return &SearchResponse{
		Provider: Keenable,
		Results:  keenableResults(out.Results),
		Queries:  []string{req.Query},
		Searches: 1,
		CostUSD:  keenableCost(out.Usage),
	}, nil
}

// keenableEndpoint is the path of an operation ("search" or "fetch") and the headers of a call. With a key: /v1/<op> and X-Api-Key.
// Without: /v1/<op>/public, which names the calling app in X-Keenable-Title and needs no key.
func keenableEndpoint(key,
	op string) (string, http.Header) {
	if key == "" {
		return "/v1/" + op + "/public", http.Header{
			"X-Keenable-Title": {keenableTitle},
		}
	}
	return "/v1/" + op, http.Header{
		"X-Api-Key": {key},
	}
}

// keenableScope turns the allowed domains into the query and the site parameter of the search: one domain goes to site, several to
// site: operators grouped in the query ("(site:a OR site:b)"). The domains are written into the query, so they must be plain host names
// or paths, with no space, quote or parenthesis.
func keenableScope(query string,
	allowed []string) (string, string, error) {
	if slices.ContainsFunc(allowed, badKeenableSite) {
		return "", "", fmt.Errorf("allowed domains must be host names or paths without spaces, quotes or parentheses: %q", allowed)
	}
	switch len(allowed) {
	case 0:
		return "", query, nil
	case 1:
		return allowed[0], query, nil
	}
	ops := mapList(allowed,
		func(d string) string {
			return "site:" + d
		})
	return "", query + " (" + strings.Join(ops, " OR ") + ")", nil
}

// badKeenableSite reports whether d cannot be written as a site in a Keenable query.
func badKeenableSite(d string) bool {
	return d == "" || strings.ContainsAny(d, " \t\r\n\"()")
}

// keenableCost is what a call cost in dollars. Only purchased credits cost money: the free monthly allowance and the keyless calls
// cost nothing.
func keenableCost(u *keenableUsage) float64 {
	if u == nil || !u.Paid {
		return 0
	}
	return u.Credits * keenableCreditUSD
}

// keenableResults maps the hits to the common results. PageAge is the publish date as Keenable gives it.
func keenableResults(in []keenableHit) []Result {
	return mapList(in,
		func(h keenableHit) Result {
			return Result{
				URL:     h.URL,
				Title:   h.Title,
				PageAge: h.PublishedAt,
				Snippet: h.Snippet,
			}
		})
}

// keenableSend sends a request to the Keenable path (with its query string, if any) and returns the body of a 200 response. A payload,
// if not nil, is sent as JSON.
func keenableSend(ctx context.Context,
	d Deps,
	method,
	path string,
	header http.Header,
	payload []byte) ([]byte, error) {
	baseURL := strings.TrimRight(d.KeenableBaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultKeenableBaseURL
	}
	httpReq, err := http.NewRequestWithContext(ctx,
		method,
		baseURL+path,
		bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header = header
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	client := d.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKeenableBytes))
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

// mapList applies f to every element of in, in order.
func mapList[T, U any](in []T,
	f func(T) U) []U {
	out := make([]U, len(in))
	for i, x := range in {
		out[i] = f(x)
	}
	return out
}

// keenableFetchResponse is the part of the 200 response of GET /v1/fetch that is used.
type keenableFetchResponse struct {
	// URL is the final address after redirects: the one to cite, and the one the domain lists are checked on again.
	URL     string         `json:"url"`
	Content string         `json:"content"`
	Usage   *keenableUsage `json:"usage"`
}

// fetchKeenable reads a page with Keenable's GET /v1/fetch: the page as markdown, with navigation and boilerplate stripped, from Keenable's
// index (or live from the source when it has no copy). Keenable follows redirects on its side, so the domain lists are checked on the
// requested host before the call and on the final host after it: text of a page on a blocked site is never returned.
func fetchKeenable(ctx context.Context,
	d Deps,
	req FetchRequest) (*FetchResponse, error) {
	requested, err := url.Parse(req.URL)
	if err != nil || (requested.Scheme != "http" && requested.Scheme != "https") || requested.Hostname() == "" {
		return nil, errors.New("url must start with http:// or https://")
	}
	if err := checkDomain(requested.Hostname(), req.AllowedDomains, req.BlockedDomains); err != nil {
		return nil, err
	}
	maxChars := req.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	query := url.Values{
		"url":       {req.URL},
		"max_chars": {strconv.Itoa(maxChars)},
	}
	path, header := keenableEndpoint(d.KeenableKey,
		"fetch")
	body, err := keenableSend(ctx,
		d,
		http.MethodGet,
		path+"?"+query.Encode(),
		header,
		nil)
	if err != nil {
		return nil, fmt.Errorf("keenable fetch %s: %w", req.URL, err)
	}
	var out keenableFetchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("keenable fetch %s: bad response: %w", req.URL, err)
	}
	final := out.URL
	if final == "" {
		final = req.URL
	}
	if u, err := url.Parse(final); err == nil {
		if err := checkDomain(u.Hostname(), req.AllowedDomains, req.BlockedDomains); err != nil {
			return nil, fmt.Errorf("keenable fetch %s: redirected: %w", req.URL, err)
		}
	}
	return &FetchResponse{
		Provider: Keenable,
		URL:      final,
		Text:     cutChars(strings.TrimSpace(out.Content), maxChars),
		CostUSD:  keenableCost(out.Usage),
	}, nil
}
