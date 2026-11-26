package remember

import (
	"errors"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// prepare reports what is wrong with a request as an *InvalidError (or an *UnavailableError for what the server does not have).
func TestPrepare(t *testing.T) {
	for _, c := range []struct {
		name string
		req  Request
		opts []memory_core.Option
		want any // *InvalidError or *UnavailableError
	}{
		{"no text", Request{Text: " "}, nil, &InvalidError{}},
		{"too large", Request{Text: strings.Repeat("a", maxRememberBytes+1)}, nil, &InvalidError{}},
		{"bad reference time", Request{Text: "t", ReferenceTime: "yesterday"}, nil, &InvalidError{}},
		{"bad kind", Request{Text: "t", Kind: "poem"}, nil, &InvalidError{}},
		{"bad model stage", Request{Text: "t", Models: map[string]string{"query": "m"}}, nil, &InvalidError{}},
		{"graph classes without classify", Request{Text: "t", ClassifyGraph: new(false), GraphClasses: []string{"a"}}, nil, &InvalidError{}},
		{"classes of an eval default to off", Request{Text: "t", GraphClasses: []string{"a"}}, []memory_core.Option{memory_core.WithEval()}, &InvalidError{}},
		{"web search without a searcher", Request{Text: "t", WebSearch: true}, nil, &UnavailableError{}},
		{"series without providers", Request{Text: "t", Timeseries: &TimeseriesRequest{Fetch: &FetchRequest{}}}, nil, &UnavailableError{}},
	} {
		_, err := prepare(t.Context(), nil, "u1", c.req, c.opts)
		var (
			invalid     *InvalidError
			unavailable *UnavailableError
		)
		switch c.want.(type) {
		case *InvalidError:
			if !errors.As(err, &invalid) {
				t.Errorf("%s: err = %v, want *InvalidError", c.name, err)
			}
		case *UnavailableError:
			if !errors.As(err, &unavailable) {
				t.Errorf("%s: err = %v, want *UnavailableError", c.name, err)
			}
		}
	}
	if _, err := prepare(t.Context(), nil, "", Request{Text: "t"}, nil); err == nil {
		t.Error("an empty user was accepted")
	}
}

// classify_graph defaults to on, and to off for a call made with WithEval; an explicit value wins.
func TestPrepareClassifyDefault(t *testing.T) {
	classify := func(req Request, opts ...memory_core.Option) bool {
		t.Helper()
		p, err := prepare(t.Context(), nil, "u1", req, opts)
		if err != nil {
			t.Fatal(err)
		}
		return memory_core.NewCallOptions(p.req.Opts).ClassifyGraph
	}
	yes, no := new(true), new(false)
	if !classify(Request{Text: "t"}) || classify(Request{Text: "t"}, memory_core.WithEval()) {
		t.Error("the default is on, and off with WithEval")
	}
	if classify(Request{Text: "t", ClassifyGraph: no}) || !classify(Request{Text: "t", ClassifyGraph: yes}, memory_core.WithEval()) {
		t.Error("an explicit classify_graph does not win")
	}
}

// code with no dataset goes to the dataset "code"; with no file name the language is detected and the name is the default of it.
func TestPrepareCodeDefaults(t *testing.T) {
	p, err := prepare(t.Context(), nil, "u1", Request{Text: "import os\n\ndef run(x):\n    return x\n", ContentType: "code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.req.Dataset != DefaultCodeDataset || p.codeName != "snippet.py" {
		t.Errorf("dataset %q, code name %q", p.req.Dataset, p.codeName)
	}
	if memory_core.NewCallOptions(p.req.Opts).Filename != "snippet.py" {
		t.Error("the default file name is not passed to the call")
	}
	p, err = prepare(t.Context(), nil, "u1", Request{Text: "x = 1", ContentType: "code", Dataset: "mine"}, nil)
	if err != nil || p.req.Dataset != "mine" {
		t.Errorf("a named dataset is kept: %v %q", err, p.req.Dataset)
	}
}

// the file name of the request does not decide the language of code, nor is it kept.
func TestPrepareCodeIgnoresFilename(t *testing.T) {
	p, err := prepare(t.Context(), nil, "u1", Request{Text: "package main\n\nfunc main() {}\n", ContentType: "code", Filename: "x.py"}, nil)
	if err != nil || p.codeName != "snippet.go" {
		t.Errorf("%v %q", err, p.codeName)
	}
}
