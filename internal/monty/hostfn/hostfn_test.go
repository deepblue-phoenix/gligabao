package hostfn

import (
	"context"
	"strings"
	"testing"
)

func ok(context.Context, Env, Args) (any, error) { return nil, nil }

func TestNewRejectsBadSpecs(t *testing.T) {
	cases := map[string][]Spec{
		"duplicate":           {{Name: "a", Result: "str", Handler: ok}, {Name: "a", Result: "str", Handler: ok}},
		"valid function":      {{Name: "Bad-Name", Result: "str", Handler: ok}},
		"no handler":          {{Name: "a", Result: "str"}},
		"result type":         {{Name: "a", Result: "set", Handler: ok}},
		"type":                {{Name: "a", Result: "str", Handler: ok, Params: []Param{{Name: "x", Type: "bytes", Required: true}}}},
		"after an optional":   {{Name: "a", Result: "str", Handler: ok, Params: []Param{{Name: "x", Type: "str", Default: ""}, {Name: "y", Type: "str", Required: true}}}},
		"needs a default":     {{Name: "a", Result: "str", Handler: ok, Params: []Param{{Name: "x", Type: "str"}}}},
		"duplicate parameter": {{Name: "a", Result: "str", Handler: ok, Params: []Param{{Name: "x", Type: "str", Required: true}, {Name: "x", Type: "str", Required: true}}}},
	}
	for want, specs := range cases {
		if _, err := New(specs...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
}

func TestStubsAndDescribe(t *testing.T) {
	r := Standard()
	wantStubs := `def notes_read() -> str: ...
def frontier_get(id: str) -> dict: ...
def frontier_list(status: str = "") -> list[dict]: ...
def claims_list(status: str = "") -> list[dict]: ...
def fetch_page(url: str, max_chars: int = 0) -> str: ...
def web_search(query: str, max_chars: int = 0) -> str: ...
def kb_query(question: str, max_chars: int = 0) -> str: ...
def log(message: str) -> None: ...
`
	if got := r.Stubs(); got != wantStubs {
		t.Errorf("stubs:\n%s", got)
	}
	if d := r.Describe(); !strings.HasPrefix(d, "- notes_read() -> str: the current notes.md text\n- frontier_get(id: str) -> dict: ") {
		t.Errorf("describe:\n%s", d)
	}
}

func TestDecode(t *testing.T) {
	spec, _ := Standard().Get("fetch_page")
	a, err := spec.Decode([]any{"u"}, map[string]any{"max_chars": float64(5)})
	if err != nil || a.Str("url") != "u" || a.Int("max_chars") != 5 {
		t.Fatalf("%v %v", a, err)
	}
	if a, _ := spec.Decode([]any{"u"}, nil); a.Int("max_chars") != 0 {
		t.Errorf("default: %v", a)
	}
	for name, c := range map[string]struct {
		args   []any
		kwargs map[string]any
	}{
		"needs":        {nil, nil},
		"at most":      {[]any{"a", 1.0, 2.0}, nil},
		"unexpected":   {[]any{"a"}, map[string]any{"zzz": 1}},
		"several":      {[]any{"a"}, map[string]any{"url": "b"}},
		"expected str": {[]any{3.0}, nil},
		"expected int": {[]any{"a", 1.5}, nil},
	} {
		if _, err := spec.Decode(c.args, c.kwargs); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOnlyNarrows(t *testing.T) {
	r, err := Standard().Only([]string{"log", "notes_read"})
	if err != nil || strings.Join(r.Names(), ",") != "notes_read,log" {
		t.Fatalf("%v %v", r.Names(), err)
	}
	if _, err := Standard().Only([]string{"nope", "zip"}); err == nil || !strings.Contains(err.Error(), "nope, zip") {
		t.Errorf("%v", err)
	}
	if all, _ := Standard().Only(nil); all.Len() != Standard().Len() {
		t.Error("an empty list keeps all")
	}
}

func TestCallReadFunctions(t *testing.T) {
	env := Env{
		Notes:     "# notes",
		Questions: []map[string]any{{"id": "q_001", "status": "open"}, {"id": "q_002", "status": "resolved"}},
		Claims:    []map[string]any{{"id": "c_001", "status": "VERIFIED"}},
	}
	r := Standard()
	ctx := context.Background()
	if v, err := r.Call(ctx, env, "notes_read", nil, nil, 0); err != nil || v != "# notes" {
		t.Errorf("%v %v", v, err)
	}
	if v, _ := r.Call(ctx, env, "frontier_list", []any{"resolved"}, nil, 0); len(v.([]map[string]any)) != 1 {
		t.Errorf("%v", v)
	}
	if v, _ := r.Call(ctx, env, "frontier_list", nil, nil, 0); len(v.([]map[string]any)) != 2 {
		t.Errorf("%v", v)
	}
	if v, err := r.Call(ctx, env, "frontier_get", nil, map[string]any{"id": "q_002"}, 0); err != nil || v.(map[string]any)["status"] != "resolved" {
		t.Errorf("%v %v", v, err)
	}
	if _, err := r.Call(ctx, env, "frontier_get", []any{"zz"}, nil, 0); err == nil {
		t.Error("an unknown question is an error")
	}
	if _, err := r.Call(ctx, env, "notes_append", nil, nil, 0); err == nil || !strings.Contains(err.Error(), "not a function you may call") {
		t.Errorf("%v", err)
	}
	var logged []string
	env.Log = func(l string) { logged = append(logged, l) }
	r.Call(ctx, env, "log", []any{"hi"}, nil, 0)
	if len(logged) != 1 || logged[0] != "hi" {
		t.Errorf("%v", logged)
	}
}

func TestCallFetchFunctions(t *testing.T) {
	var gotTool string
	var gotIn map[string]any
	env := Env{Fetch: func(_ context.Context, tool string, in map[string]any) (string, error) {
		gotTool, gotIn = tool, in
		return "0123456789", nil
	}}
	r := Standard()
	v, err := r.Call(context.Background(), env, "fetch_page", []any{"https://a.com"}, map[string]any{"max_chars": 4.0}, 0)
	if err != nil || v != "0123" || gotTool != "fetch_page" || gotIn["url"] != "https://a.com" {
		t.Errorf("%v %v %s %v", v, err, gotTool, gotIn)
	}
	r.Call(context.Background(), env, "kb_query", []any{"why"}, nil, 0)
	if gotTool != "query" || gotIn["question"] != "why" {
		t.Errorf("%s %v", gotTool, gotIn)
	}
	if _, err := r.Call(context.Background(), Env{}, "web_search", []any{"q"}, nil, 0); err == nil {
		t.Error("no fetch in the env is an error")
	}
}

func TestCallRefusesBigResults(t *testing.T) {
	env := Env{Notes: strings.Repeat("x", 500)}
	_, err := Standard().Call(context.Background(), env, "notes_read", nil, nil, 100)
	if err == nil || !strings.Contains(err.Error(), "over the limit of 100") {
		t.Errorf("%v", err)
	}
}
