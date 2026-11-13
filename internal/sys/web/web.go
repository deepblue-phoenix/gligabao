// Package web is the one entry point for the two web tools of the agents: Search and Fetch. The caller names the provider
// (Provider) that does the work, and gets the same request and response types whichever it is.
//
// The providers are not alike. Anthropic's web search is a "server tool": the searches run in Anthropic's cloud, inside a model call,
// and what comes back is the model's written answer with its sources (Answer and Results of the response, with tokens in the cost).
// The others are plain HTTP APIs that gligabao calls itself and that answer with results (and, for some, an answer too). The wrapper
// hides this: Answer may be empty, Results may carry a snippet, and CostUSD is what the call cost, in tokens or in fees.
package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/llm"
)

// Provider says which service does a search or a fetch.
type Provider string

const (
	// Anthropic is Anthropic's cloud web search (Search only for now: see Fetch).
	Anthropic Provider = "anthropic"
	// Linkup is the Linkup API (Search and Fetch).
	Linkup Provider = "linkup"
	// Keenable is the Keenable API (not implemented yet).
	Keenable Provider = "keenable"
	// Local reads a page with a plain HTTP GET from this process, no third party (Fetch only).
	Local Provider = "local"
)

var (
	// ErrNotImplemented is returned for a provider that is known but has no code for this operation yet.
	ErrNotImplemented = errors.New("web: provider not implemented")
	// ErrUnknownProvider is returned for a Provider that is none of the constants.
	ErrUnknownProvider = errors.New("web: unknown provider")
)

// Deps is what the providers need. Each provider uses only its part: LLM for Anthropic, HTTP for Local (and for the HTTP APIs).
type Deps struct {
	// LLM is the Anthropic client (Anthropic search).
	LLM *llm.Client
	// HTTP reads pages (Local fetch). Nil uses a client of its own with the default transport.
	HTTP *http.Client
	// LinkupKey is the Linkup API key (from the config). LinkupBaseURL overrides the API root (tests).
	LinkupKey, LinkupBaseURL string
}

// Location is the approximate place a search is made from. Country is an ISO 3166-1 alpha-2 code, Timezone an IANA name.
type Location struct {
	Country, Region, City, Timezone string
}

// SearchRequest asks for a web search. Settings a provider has no use for are ignored by it.
type SearchRequest struct {
	Query string
	// AllowedDomains restricts the search to these sites, BlockedDomains keeps it off others (host names; a subdomain matches).
	AllowedDomains, BlockedDomains []string
	Location                       *Location
	// System, Model, MaxTokens and MaxUses are for Anthropic, where a model runs the searches: the system prompt, the model (empty: the
	// client's), the bound of the answer (0: the client's) and the most searches it makes (0: the llm default).
	System    string
	Model     string
	MaxTokens int
	MaxUses   int
}

// Result is one page a search found.
type Result struct {
	URL   string
	Title string
	// PageAge is how old the page is, as the provider says; empty if unknown.
	PageAge string
	// Snippet is an excerpt of the page, for providers that return one.
	Snippet string
}

// SearchResponse is the outcome of a search.
type SearchResponse struct {
	Provider Provider
	// Answer is a written summary of what was found; empty if the provider gives only results.
	Answer  string
	Results []Result
	// Queries are the queries sent to the search engine, if known. Searches is how many were made and billed.
	Queries  []string
	Searches int
	Model    string
	// InputTokens and OutputTokens are the tokens of the model that ran the search (0 for providers without one).
	InputTokens, OutputTokens int
	// CostUSD is what the call cost: tokens plus fees.
	CostUSD float64
}

// FetchRequest asks for the text of a page.
type FetchRequest struct {
	URL string
	// MaxChars cuts the text; 0 uses DefaultMaxChars.
	MaxChars int
	// AllowedDomains, if not empty, are the only sites that may be read (and redirected to); BlockedDomains are never read. Enforced
	// for every provider.
	AllowedDomains, BlockedDomains []string
}

// FetchResponse is the outcome of a fetch.
type FetchResponse struct {
	Provider Provider
	URL      string
	Text     string
	CostUSD  float64
}

// implementedSearch and implementedFetch are the providers that have code for the operation (the others return ErrNotImplemented).
var (
	implementedSearch = []Provider{Anthropic, Linkup}
	implementedFetch  = []Provider{Local, Linkup}
)

// ParseSearch reads the name of a search provider (case and spaces do not matter); an empty name is def. A name that is not a provider, or a
// provider that cannot search yet, is an error that says which are usable.
func ParseSearch(name string,
	def Provider) (Provider, error) {
	return parse("search", name, def, implementedSearch)
}

// ParseFetch is ParseSearch for the provider of fetch.
func ParseFetch(name string,
	def Provider) (Provider, error) {
	return parse("fetch", name, def, implementedFetch)
}

func parse(op,
	name string,
	def Provider,
	usable []Provider) (Provider, error) {
	p := Provider(strings.ToLower(strings.TrimSpace(name)))
	if p == "" {
		return def, nil
	}
	if slices.Contains(usable, p) {
		return p, nil
	}
	names := make([]string, len(usable))
	for i, u := range usable {
		names[i] = string(u)
	}
	if slices.Contains([]Provider{Anthropic, Linkup, Keenable, Local}, p) {
		return "", fmt.Errorf("the %s provider %q is not available yet (available: %s)", op, p, strings.Join(names, ", "))
	}
	return "", fmt.Errorf("%q is not a %s provider (available: %s)", name, op, strings.Join(names, ", "))
}

// Search runs a web search with the provider p.
func Search(ctx context.Context,
	p Provider,
	d Deps,
	req SearchRequest) (*SearchResponse, error) {
	switch p {
	case Anthropic:
		return searchAnthropic(ctx, d, req)
	case Linkup:
		return searchLinkup(ctx, d, req)
	case Keenable:
		return nil, fmt.Errorf("%w: %s search", ErrNotImplemented, p)
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, p)
}

// Fetch reads a page with the provider p. The domain lists of the request are checked here, before any provider is called.
func Fetch(ctx context.Context,
	p Provider,
	d Deps,
	req FetchRequest) (*FetchResponse, error) {
	switch p {
	case Local:
		return fetchLocal(ctx, d, req)
	case Linkup:
		return fetchLinkup(ctx, d, req)
	case Anthropic, Keenable:
		return nil, fmt.Errorf("%w: %s fetch", ErrNotImplemented, p)
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, p)
}
