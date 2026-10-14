package memory_core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func decodeSeries(t *testing.T, js string) []SeriesSpec {
	t.Helper()
	var s []SeriesSpec
	if err := json.Unmarshal([]byte(js), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestResolveSeries(t *testing.T) {
	in := decodeSeries(t, `[{"key":"  AAPL.close ","unit":"USD","points":[
		["2025-01-31T00:00:00Z",3],["2025-01-29T00:00:00Z",1],["2025-01-31T00:00:00Z",4]],
		"anchors":[{"entity":{"name":" Apple ","type":"ORGANIZATION"}},{"edge":{"source":"A","relation":"Works  At","target":"B"}},{"entity_id":7}]}]`)
	out, err := ResolveSeries(in, true)
	if err != nil {
		t.Fatal(err)
	}
	s := out[0]
	if s.Key != "AAPL.close" || s.Field != "value" || s.Name != "AAPL.close" || len(s.Points) != 2 {
		t.Fatalf("resolved: %+v", s)
	}
	if !s.Points[0].TS.Before(s.Points[1].TS) || s.Points[1].Value != 4 { // sorted; the last value for a time wins
		t.Errorf("points: %+v", s.Points)
	}
	a := s.Anchors
	if a[0].Entity.Name != "Apple" || a[0].Entity.Type != "organization" || a[0].OnUnmatched != "create" ||
		a[1].Edge.Relation != "works_at" || a[1].OnUnmatched != "ignore" || a[2].OnUnmatched != "error" {
		t.Errorf("anchors: %+v %+v %+v", a[0], a[1], a[2])
	}
	// Resolving a resolved series changes nothing.
	again, err := ResolveSeries(out, true)
	if err != nil || len(again[0].Points) != 2 || again[0].Anchors[0].OnUnmatched != "create" {
		t.Errorf("idempotence: %+v %v", again, err)
	}
	if out, err := ResolveSeries(nil, true); out != nil || err != nil {
		t.Errorf("nil: %v %v", out, err)
	}
}

func TestResolveSeriesWindow(t *testing.T) {
	out, err := ResolveSeries(decodeSeries(t, `[{"key":"k","points":[["2025-01-31T00:00:00Z",1]],
		"anchors":[{"entity":{"name":"a"},"window":["2025-01-01T00:00:00Z","2025-02-01T00:00:00Z"]}]}]`), true)
	if err != nil {
		t.Fatal(err)
	}
	if a := out[0].Anchors[0]; a.WindowStart == nil || a.WindowEnd == nil || a.WindowEnd.Sub(*a.WindowStart).Hours() != 31*24 {
		t.Errorf("window: %+v", a)
	}
}

func TestResolveSeriesInvalid(t *testing.T) {
	const pt = `"points":[["2025-01-31T00:00:00Z",1]]`
	many := make([]SeriesSpec, MaxSeriesPerCall+1)
	if _, err := ResolveSeries(many, true); !errors.Is(err, ErrInvalidSeries) {
		t.Errorf("too many series: %v", err)
	}
	for name, js := range map[string]string{
		"no key":          `[{` + pt + `}]`,
		"blank key":       `[{"key":"  ",` + pt + `}]`,
		"control char":    `[{"key":"a\u0001b",` + pt + `}]`,
		"long key":        `[{"key":"` + strings.Repeat("k", MaxSeriesKeyLen+1) + `",` + pt + `}]`,
		"no points":       `[{"key":"k"}]`,
		"zero time":       `[{"key":"k","points":[["0001-01-01T00:00:00Z",1]]}]`,
		"duplicate key":   `[{"key":"k",` + pt + `},{"key":"k","field":"value",` + pt + `}]`,
		"entity no name":  `[{"key":"k",` + pt + `,"anchors":[{"entity":{"type":"x"}}]}]`,
		"edge no target":  `[{"key":"k",` + pt + `,"anchors":[{"edge":{"source":"a","relation":"r"}}]}]`,
		"artifact+entity": `[{"key":"k",` + pt + `,"anchors":[{"artifact":true,"entity":{"name":"a"}}]}]`,
		"bad on_unmatch":  `[{"key":"k",` + pt + `,"anchors":[{"entity":{"name":"a"},"on_unmatched":"maybe"}]}]`,
		"create for id":   `[{"key":"k",` + pt + `,"anchors":[{"entity_id":1,"on_unmatched":"create"}]}]`,
		"negative id":     `[{"key":"k",` + pt + `,"anchors":[{"entity_id":-1}]}]`,
		"one-time window": `[{"key":"k",` + pt + `,"anchors":[{"entity":{"name":"a"},"window":["2025-01-01T00:00:00Z"]}]}]`,
	} {
		if _, err := ResolveSeries(decodeSeries(t, js), true); !errors.Is(err, ErrInvalidSeries) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// "artifact" needs an artifact to anchor to.
	if _, err := ResolveSeries(decodeSeries(t, `[{"key":"k",`+pt+`,"anchors":[{"artifact":true}]}]`), false); !errors.Is(err, ErrInvalidSeries) {
		t.Errorf("artifact without artifact: %v", err)
	}
	// The point decoder.
	for _, js := range []string{`[{"key":"k","points":[["2025-01-31",1]]}]`, `[{"key":"k","points":[[1,2]]}]`, `[{"key":"k","points":[["2025-01-31T00:00:00Z","x"]]}]`, `[{"key":"k","points":[1]}]`} {
		var s []SeriesSpec
		if err := json.Unmarshal([]byte(js), &s); err == nil {
			t.Errorf("%s decoded", js)
		}
	}
}
