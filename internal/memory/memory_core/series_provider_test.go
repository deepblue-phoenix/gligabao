package memory_core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func okFetch(context.Context, SeriesFetchRequest) ([]SeriesData, error) { return nil, nil }

func TestResolveSeriesProviders(t *testing.T) {
	got, err := ResolveSeriesProviders([]SeriesProvider{
		{Name: " b.prices-1 ", Description: " Prices. ", Fetch: okFetch},
		{Name: "a", Description: "Macro.", QueryFormat: " series id ", Fetch: okFetch},
	})
	if err != nil || len(got) != 2 || got[0].Name != "a" || got[1].Name != "b.prices-1" || got[1].Description != "Prices." || got[0].QueryFormat != "series id" {
		t.Fatalf("got %+v, %v", got, err)
	}
	for name, ps := range map[string][]SeriesProvider{
		"no name":        {{Description: "d", Fetch: okFetch}},
		"bad name":       {{Name: "a b", Description: "d", Fetch: okFetch}},
		"no description": {{Name: "a", Fetch: okFetch}},
		"no fetch":       {{Name: "a", Description: "d"}},
		"twice":          {{Name: "a", Description: "d", Fetch: okFetch}, {Name: "a", Description: "e", Fetch: okFetch}},
		"long":           {{Name: "a", Description: strings.Repeat("x", MaxProviderDescriptionLen+1), Fetch: okFetch}},
	} {
		if _, err := ResolveSeriesProviders(ps); !errors.Is(err, ErrInvalidProvider) {
			t.Errorf("%s: err %v", name, err)
		}
	}
	if got, err := ResolveSeriesProviders(nil); err != nil || len(got) != 0 {
		t.Errorf("none: %v, %v", got, err)
	}
}

func TestResolveSeriesFetch(t *testing.T) {
	ps := []SeriesProvider{{Name: "a"}, {Name: "b"}}
	if f, err := ResolveSeriesFetch(nil, ps); f != nil || err != nil {
		t.Errorf("nil: %v, %v", f, err)
	}
	if _, err := ResolveSeriesFetch(&SeriesFetch{}, nil); !errors.Is(err, ErrSeriesProvidersUnavailable) {
		t.Errorf("no providers: %v", err)
	}
	f, err := ResolveSeriesFetch(&SeriesFetch{}, ps)
	if err != nil || len(f.Providers) != 2 || f.Before != DefaultFetchBefore || f.After != 0 {
		t.Errorf("defaults: %+v, %v", f, err)
	}
	f, err = ResolveSeriesFetch(&SeriesFetch{Providers: []string{"b", " b "}, Before: time.Hour, After: time.Hour}, ps)
	if err != nil || len(f.Providers) != 1 || f.Providers[0] != "b" || f.Before != time.Hour || f.After != time.Hour {
		t.Errorf("named: %+v, %v", f, err)
	}
	for name, in := range map[string]*SeriesFetch{
		"unknown": {Providers: []string{"c"}},
		"neg":     {Before: -1},
		"huge":    {After: MaxFetchWindow + 1},
	} {
		if _, err := ResolveSeriesFetch(in, ps); !errors.Is(err, ErrInvalidSeries) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParseFetchWindow(t *testing.T) {
	day := 24 * time.Hour
	for in, want := range map[string]time.Duration{
		"": 0, "0": 0, "30d": 30 * day, "2w": 14 * day, "6mo": 180 * day, "1y": 365 * day, "1.5d": 36 * time.Hour,
		"90m": 90 * time.Minute, " 7D ": 7 * day, "1h30m": 90 * time.Minute,
	} {
		if got, err := ParseFetchWindow(in); err != nil || got != want {
			t.Errorf("%q: %v, %v (want %v)", in, got, err, want)
		}
	}
	for _, in := range []string{"soon", "-3d", "d", "99999y", "1x"} {
		if _, err := ParseFetchWindow(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}
