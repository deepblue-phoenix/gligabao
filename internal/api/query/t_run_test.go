package query

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// The reasoning field of a request is an object with a level per stage, or a string: the level of the answering call.
func TestReasoningJSON(t *testing.T) {
	for _, c := range []struct {
		body string
		want Reasoning
		bad  bool
	}{
		{body: `{}`},
		{body: `{"reasoning":null}`},
		{body: `{"reasoning":"low"}`, want: Reasoning{"query": "low"}},
		{body: `{"reasoning":{"query":"high","detect_geo":"none"}}`, want: Reasoning{"query": "high", "detect_geo": "none"}},
		{body: `{"reasoning":7}`, bad: true},
		{body: `{"reasoning":{"query":7}}`, bad: true},
	} {
		var req Request
		err := json.Unmarshal([]byte(c.body), &req)
		if c.bad {
			if !errors.Is(err, memory_core.ErrInvalidReasoning) {
				t.Errorf("%s: err = %v, want ErrInvalidReasoning", c.body, err)
			}
			continue
		}
		if err != nil || len(req.Reasoning) != len(c.want) {
			t.Errorf("%s: %v, %v; want %v", c.body, req.Reasoning, err, c.want)
			continue
		}
		for k, v := range c.want {
			if req.Reasoning[k] != v {
				t.Errorf("%s: %v, want %v", c.body, req.Reasoning, c.want)
			}
		}
	}
}

// prepare reports what is wrong with a request as an *InvalidError, and makes the options of a valid one.
func TestPrepare(t *testing.T) {
	for _, c := range []struct {
		name string
		req  Request
	}{
		{"no question", Request{Query: "  "}},
		{"bad session", Request{Query: "q", SessionID: "a\tb"}},
		{"bad reasoning", Request{Query: "q", Reasoning: Reasoning{"extract_graph": "low"}}},
		{"bad model stage", Request{Query: "q", Models: map[string]string{"extract_graph": "m"}}},
		{"graph index without hook", Request{Query: "q", GraphIndex: "idx"}},
		{"radius with nearby off", Request{Query: "q", GeoNearby: new(false), GeoRadiusKm: new(5.0)}},
		{"nearby without geo", Request{Query: "q", GeoNearby: new(true)}},
	} {
		_, err := prepare(c.req, nil)
		var invalid *InvalidError
		if !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want *InvalidError", c.name, err)
		}
	}
	p, err := prepare(Request{Query: " Where? ", SessionID: " s1 "}, nil)
	if err != nil || p.query != "Where?" || p.sessionID != "s1" {
		t.Errorf("valid request: %+v, %v", p, err)
	}
}
