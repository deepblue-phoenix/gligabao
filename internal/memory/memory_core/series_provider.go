package memory_core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Timeseries providers (see docs/timeseries.md): sources of timeseries that the program embedding gligabao registers
// when it starts the server (gligabao.ServeOptions.TimeseriesProviders). A provider describes, in natural language,
// what it provides and when it applies; an LLM reads those descriptions and decides which provider to use for which
// entity of a remembered text (the "resolve_series" stage). The provider's Fetch callback then does the fetching, so
// the embedding program owns all the logic that talks to the data source (credentials, symbols, rate limits).

// ErrInvalidProvider means a registered provider is malformed.
var ErrInvalidProvider = errors.New("invalid timeseries provider")

// ErrSeriesProvidersUnavailable means a call asked for series to be fetched but no provider is registered.
var ErrSeriesProvidersUnavailable = errors.New("timeseries fetch is not available: no timeseries provider is registered")

// The limits of a provider and of a fetch window.
const (
	MaxProviderNameLen        = 64
	MaxProviderDescriptionLen = 4000
	MaxProviderQueryLen       = 1000
	MaxProviders              = 50
	// MaxFetchWindow is the longest time before or after the reference time a fetch may ask for.
	MaxFetchWindow = 20 * 365 * 24 * time.Hour
	// DefaultFetchBefore is how far before the reference time a fetch reaches when the request says nothing. The default
	// after is 0: nothing past the reference time is fetched, so a memory of an event never contains its future.
	DefaultFetchBefore = 30 * 24 * time.Hour
)

var providerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// FetchEntity is the entity a series is fetched for.
type FetchEntity struct {
	Name        string
	Type        string // e.g. "organization", "person", "location", "concept"
	Description string // what the text says about the entity; may be empty
}

// SeriesFetchRequest is what a provider's Fetch callback is asked for.
type SeriesFetchRequest struct {
	Entity FetchEntity
	// Query is what the LLM chose to identify the entity to this provider, following the provider's QueryFormat (e.g. a
	// ticker symbol, "AAPL"). It is a suggestion made by a model: validate it, and return nothing if it is not usable.
	Query string
	// From and To (UTC) are the period wanted, inclusive: the reference time of the text, minus and plus the window of
	// the request. Points outside are dropped by gligabao.
	From, To time.Time
	// ReferenceTime is when the remembered text was written (or the current time).
	ReferenceTime time.Time
}

// SeriesData is a series returned by a provider. It is validated like a supplied series (see SeriesSpec): a key, an
// optional field (one provider call may return several, e.g. the open, high, low and close of a price), at least one
// point.
type SeriesData struct {
	Key       string
	Field     string
	Name      string
	Unit      string
	Frequency string
	Points    []SeriesPoint
}

// SeriesFetchFunc fetches the series of one entity. It returns no series and no error when it has nothing for the
// entity (a normal outcome); an error when the fetch failed. It must honour ctx, and may be called concurrently.
type SeriesFetchFunc func(ctx context.Context, req SeriesFetchRequest) ([]SeriesData, error)

// SeriesProvider is a source of timeseries.
type SeriesProvider struct {
	// Name identifies the provider: letters, digits, '_', '-' and '.', unique. It is what a request names in
	// timeseries.fetch.providers and what is stored as the provider of the series it fetched.
	Name string
	// Description says in natural language what the provider offers and, above all, WHEN it should be used, so an LLM
	// can decide whether an entity is a case for it: "Daily stock prices and volumes. Use for companies whose shares
	// are publicly traded on a stock exchange; not for private companies, people or places."
	Description string
	// QueryFormat says what the Fetch callback needs to identify an entity, which the LLM then supplies as
	// SeriesFetchRequest.Query: "the ticker symbol on its main exchange, e.g. AAPL". Optional; without it the query is
	// the entity's name.
	QueryFormat string
	// Fetch is the callback that fetches the series. Required.
	Fetch SeriesFetchFunc
}

// ResolveSeriesProviders validates the providers and returns a normalized copy (strings trimmed), sorted by name.
func ResolveSeriesProviders(in []SeriesProvider) ([]SeriesProvider, error) {
	if len(in) > MaxProviders {
		return nil, fmt.Errorf("%w: at most %d providers", ErrInvalidProvider, MaxProviders)
	}
	out := make([]SeriesProvider, 0, len(in))
	seen := map[string]bool{}
	for i, p := range in {
		p.Name, p.Description, p.QueryFormat = strings.TrimSpace(p.Name), strings.TrimSpace(p.Description), strings.TrimSpace(p.QueryFormat)
		switch {
		case p.Name == "" || len(p.Name) > MaxProviderNameLen || !providerNameRE.MatchString(p.Name):
			return nil, fmt.Errorf("%w: providers[%d]: name %q must be 1-%d letters, digits, '_', '-' or '.'", ErrInvalidProvider, i, p.Name, MaxProviderNameLen)
		case seen[p.Name]:
			return nil, fmt.Errorf("%w: provider %q is registered twice", ErrInvalidProvider, p.Name)
		case p.Description == "":
			return nil, fmt.Errorf("%w: provider %q needs a description (it tells the LLM when to use it)", ErrInvalidProvider, p.Name)
		case len(p.Description) > MaxProviderDescriptionLen:
			return nil, fmt.Errorf("%w: provider %q: the description is longer than %d bytes", ErrInvalidProvider, p.Name, MaxProviderDescriptionLen)
		case len(p.QueryFormat) > MaxProviderQueryLen:
			return nil, fmt.Errorf("%w: provider %q: the query format is longer than %d bytes", ErrInvalidProvider, p.Name, MaxProviderQueryLen)
		case p.Fetch == nil:
			return nil, fmt.Errorf("%w: provider %q has no Fetch callback", ErrInvalidProvider, p.Name)
		}
		seen[p.Name] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

//---------------------------------------------------------------------------------------------

// SeriesFetch asks Remember to fetch series from the registered providers for the entities of the text (see
// WithSeriesFetch). It is opt-in: fetching costs time and, for a provider, perhaps money.
type SeriesFetch struct {
	// Providers are the names of the providers that may be used; empty means all registered.
	Providers []string
	// Before and After are how far before and after the reference time the series reach. Zero Before means
	// DefaultFetchBefore; zero After means nothing after the reference time.
	Before, After time.Duration
}

// ResolveSeriesFetch validates f against the registered providers and returns a normalized copy: provider names
// checked and de-duplicated (all of them if none is named), the default window applied. It returns
// ErrSeriesProvidersUnavailable if there is no provider, ErrInvalidSeries for an unknown name or a window out of range.
// A nil f gives nil.
func ResolveSeriesFetch(f *SeriesFetch, providers []SeriesProvider) (*SeriesFetch, error) {
	if f == nil {
		return nil, nil
	}
	if len(providers) == 0 {
		return nil, ErrSeriesProvidersUnavailable
	}
	known := map[string]bool{}
	var all []string
	for _, p := range providers {
		known[p.Name] = true
		all = append(all, p.Name)
	}
	out := &SeriesFetch{Before: f.Before, After: f.After}
	seen := map[string]bool{}
	for _, n := range f.Providers {
		n = strings.TrimSpace(n)
		if !known[n] {
			return nil, fmt.Errorf("%w: fetch: unknown provider %q (registered: %s)", ErrInvalidSeries, n, strings.Join(all, ", "))
		}
		if !seen[n] {
			seen[n] = true
			out.Providers = append(out.Providers, n)
		}
	}
	if len(out.Providers) == 0 {
		out.Providers = all
	}
	if out.Before < 0 || out.After < 0 || out.Before > MaxFetchWindow || out.After > MaxFetchWindow {
		return nil, fmt.Errorf("%w: fetch: before and after must be between 0 and 20 years", ErrInvalidSeries)
	}
	if out.Before == 0 {
		out.Before = DefaultFetchBefore
	}
	return out, nil
}

// ParseFetchWindow parses a duration of a fetch window: a number and a unit, "90m", "36h", "30d", "2w", "6mo" (30 days
// each) or "1y" (365 days); Go durations such as "1h30m" also work. "" and "0" are 0.
func ParseFetchWindow(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "0" {
		return 0, nil
	}
	units := []struct {
		suffix string
		d      time.Duration
	}{{"mo", 30 * 24 * time.Hour}, {"d", 24 * time.Hour}, {"w", 7 * 24 * time.Hour}, {"y", 365 * 24 * time.Hour}}
	for _, u := range units {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			v, err := strconv.ParseFloat(n, 64)
			if err != nil || v < 0 || v > float64(MaxFetchWindow/u.d) {
				return 0, fmt.Errorf("invalid window %q (examples: 30d, 2w, 6mo, 1y)", s)
			}
			return time.Duration(v * float64(u.d)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid window %q (examples: 30d, 2w, 6mo, 1y)", s)
	}
	return d, nil
}

// WithSeriesProviders registers the timeseries providers available to Remember (see SeriesProvider); they are used only
// by the calls that ask for it (WithSeriesFetch). Validate them with ResolveSeriesProviders first.
func WithSeriesProviders(p []SeriesProvider) Option {
	return func(o *CallOptions) { o.SeriesProviders = p }
}

// WithSeriesObservations makes Remember derive an artifact of kind series_observation from each timeseries it stores (the
// supplied ones and the fetched ones; at most 10 per call): a text generated deterministically from the series' stats
// over the window of this call, which is extracted, reconciled and stored like any text, so the series reaches the graph
// as facts. It costs one extraction call per observation. Without series (WithSeries, WithSeriesFetch) there is nothing
// to observe. See docs/timeseries.md.
func WithSeriesObservations(on bool) Option {
	return func(o *CallOptions) { o.SeriesObservations = on }
}

// WithSeriesFetch makes Remember fetch timeseries from the registered providers for the entities of the text: an LLM
// (the resolve_series stage) decides which provider is used for which entity, from the providers' descriptions. The
// series are stored after the text, anchored to the entity and to the artifact the entity came from. Remember returns
// ErrSeriesProvidersUnavailable if there is no provider (WithSeriesProviders). Validate f with ResolveSeriesFetch.
func WithSeriesFetch(f *SeriesFetch) Option { return func(o *CallOptions) { o.SeriesFetch = f } }
