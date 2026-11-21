package evalfile

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func testLimits() Limits {
	return DefaultLimits([]string{"claude-haiku-4-5", "gpt-5"},
		[]string{"extract_graph", "query"},
		[]string{"extract_graph", "query"})
}

// evalJSON is an eval named "e" with the given cases (raw JSON objects).
func evalJSON(cases ...string) []byte {
	return []byte(`{"name":"e","description":"d","cases":[` + strings.Join(cases, ",") + `]}`)
}

const okCase = `{"name":"c1","remember":[{"text":"Alice met Bob."}],"query":"Who met Bob?","expect_contains":["Alice"]}`

func problemsOf(data []byte,
	name string,
	l Limits) string {
	ps := ValidateUser(data, name, l)
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return strings.Join(out, "\n")
}

func TestValidateUserValid(t *testing.T) {
	full := `{"name":"c2","remember":[
		{"text":["line one","line two"],"reference_time":"2026-01-02T03:04:05Z","kind":"episode","web_search":true,"timeseries":{"a":1}},
		{"text_file":"corpus/doc.txt"}],
		"query":"q","expect_edges":[{"source":"a","target":"b","ended":false}],"expect_min_chunks":2,"chunk_size":200,
		"models":{"extract_graph":"gpt-5"},"reasoning":{"query":"high","extract_graph":"none"}}`
	if got := problemsOf(evalJSON(okCase, full), "e", testLimits()); got != "" {
		t.Fatalf("problems: %s", got)
	}
	for _, name := range []string{"a", "A1", "my-eval_2.v1", strings.Repeat("a", 64)} {
		data := []byte(fmt.Sprintf(`{"name":%q,"cases":[%s]}`, name, okCase))
		if got := problemsOf(data, name, testLimits()); got != "" {
			t.Errorf("name %q: %s", name, got)
		}
	}
}

func TestValidateUserRules(t *testing.T) {
	cases := []struct {
		what string
		data []byte
		name string
		want string // a substring of the problems
	}{
		{"not json", []byte(`{`), "e", "not a valid eval file"},
		{"two values", []byte(`{"name":"e","cases":[]} {}`), "e", "more than one"},
		{"unknown key", []byte(`{"name":"e","cases":[],"nope":1}`), "e", "unknown field"},
		{"unknown case key", evalJSON(`{"name":"c","query":"q","expect_contain":["x"]}`), "e", "unknown field"},
		{"wrong shape", []byte(`{"name":"e","cases":{}}`), "e", "not a valid eval file"},
		{"name mismatch", evalJSON(okCase), "other", "must be the name of the eval"},
		{"no cases", evalJSON(), "e", "no cases"},
		{"empty name", []byte(`{"name":"","cases":[` + okCase + `]}`), "", "eval name"},
		{"slash in name", []byte(`{"name":"a/b","cases":[` + okCase + `]}`), "a/b", "eval name"},
		{"dotdot in name", []byte(`{"name":"a..b","cases":[` + okCase + `]}`), "a..b", "eval name"},
		{"leading dot name", []byte(`{"name":".a","cases":[` + okCase + `]}`), ".a", "eval name"},
		{"long name", []byte(`{"name":"` + strings.Repeat("a", 65) + `","cases":[` + okCase + `]}`), strings.Repeat("a", 65), "eval name"},
		{"reserved runs", []byte(`{"name":"runs","cases":[` + okCase + `]}`), "runs", "reserved"},
		{"reserved case_runs", []byte(`{"name":"case_runs","cases":[` + okCase + `]}`), "case_runs", "reserved"},
		{"reserved trajectories", []byte(`{"name":"trajectories","cases":[` + okCase + `]}`), "trajectories", "reserved"},
		{"case without name", evalJSON(`{"query":"q"}`), "e", "has no name"},
		{"bad case name", evalJSON(`{"name":"a b","query":"q"}`), "e", "is not valid"},
		{"dotdot case name", evalJSON(`{"name":"a..b","query":"q"}`), "e", "is not valid"},
		{"duplicate case", evalJSON(okCase, okCase), "e", "duplicate case name"},
		{"nothing to do", evalJSON(`{"name":"c"}`), "e", "nothing to do"},
		{"text and file", evalJSON(`{"name":"c","remember":[{"text":"a","text_file":"a.txt"}]}`), "e", "both text and text_file"},
		{"neither", evalJSON(`{"name":"c","remember":[{"web_search":true}]}`), "e", "needs text or text_file"},
		{"empty text list", evalJSON(`{"name":"c","remember":[{"text":[]}]}`), "e", "needs text or text_file"},
		{"text type", evalJSON(`{"name":"c","remember":[{"text":3}]}`), "e", "not a valid eval file"},
		{"bad kind", evalJSON(`{"name":"c","remember":[{"text":"a","kind":"x"}]}`), "e", "kind"},
		{"bad time", evalJSON(`{"name":"c","remember":[{"text":"a","reference_time":"yesterday"}]}`), "e", "RFC 3339"},
		{"abs text_file", evalJSON(`{"name":"c","remember":[{"text_file":"/etc/passwd"}]}`), "e", "text_file"},
		{"dotdot text_file", evalJSON(`{"name":"c","remember":[{"text_file":"../a.txt"}]}`), "e", "text_file"},
		{"dotdot mid text_file", evalJSON(`{"name":"c","remember":[{"text_file":"a/../b.txt"}]}`), "e", "text_file"},
		{"double slash text_file", evalJSON(`{"name":"c","remember":[{"text_file":"a//b.txt"}]}`), "e", "text_file"},
		{"dot segment text_file", evalJSON(`{"name":"c","remember":[{"text_file":"a/./b.txt"}]}`), "e", "text_file"},
		{"trailing slash text_file", evalJSON(`{"name":"c","remember":[{"text_file":"a/"}]}`), "e", "text_file"},
		{"space text_file", evalJSON(`{"name":"c","remember":[{"text_file":"a b.txt"}]}`), "e", "text_file"},
		{"long text_file", evalJSON(`{"name":"c","remember":[{"text_file":"` + strings.Repeat("a", 201) + `"}]}`), "e", "text_file"},
		{"eval json as text_file", evalJSON(`{"name":"c","remember":[{"text_file":"other.json"}]}`), "e", "text_file"},
		{"bad chunk size", evalJSON(`{"name":"c","query":"q","chunk_size":5}`), "e", "chunk_size"},
		{"negative chunk size", evalJSON(`{"name":"c","query":"q","chunk_size":-1}`), "e", "chunk_size"},
		{"unknown model", evalJSON(`{"name":"c","query":"q","models":{"query":"gpt-9"}}`), "e", "not a supported model"},
		{"unknown model stage", evalJSON(`{"name":"c","query":"q","models":{"nope":"gpt-5"}}`), "e", "not a stage"},
		{"bad level", evalJSON(`{"name":"c","query":"q","reasoning":{"query":"extreme"}}`), "e", "not a level"},
		{"empty level", evalJSON(`{"name":"c","query":"q","reasoning":{"query":""}}`), "e", "not a level"},
		{"unknown reasoning stage", evalJSON(`{"name":"c","query":"q","reasoning":{"nope":"low"}}`), "e", "not a stage"},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			got := problemsOf(tc.data, tc.name, testLimits())
			if !strings.Contains(got, tc.want) {
				t.Fatalf("problems = %q, want one with %q", got, tc.want)
			}
		})
	}
}

func TestValidateUserLimits(t *testing.T) {
	l := testLimits()
	l.MaxFileBytes = 400
	l.MaxCases = 2
	l.MaxRemember = 2
	l.MaxTextBytes = 10
	l.MaxQueryBytes = 5
	for what, tc := range map[string]struct {
		data []byte
		want string
	}{
		"file size":  {evalJSON(`{"name":"c","query":"` + strings.Repeat("q", 500) + `"}`), "limit is 400"},
		"cases":      {evalJSON(`{"name":"a","query":"q"}`, `{"name":"b","query":"q"}`, `{"name":"c","query":"q"}`), "3 cases, the limit is 2"},
		"remember":   {evalJSON(`{"name":"c","remember":[{"text":"a"},{"text":"b"},{"text":"c"}]}`), "3 remember items"},
		"text size":  {evalJSON(`{"name":"c","remember":[{"text":"` + strings.Repeat("a", 11) + `"}]}`), "the text is 11 bytes"},
		"joined":     {evalJSON(`{"name":"c","remember":[{"text":["aaaaaa","bbbbbb"]}]}`), "the text is 13 bytes"},
		"query size": {evalJSON(`{"name":"c","query":"abcdef"}`), "the query is 6 bytes"},
	} {
		if got := problemsOf(tc.data, "e", l); !strings.Contains(got, tc.want) {
			t.Errorf("%s: problems = %q, want %q", what, got, tc.want)
		}
	}
	// exactly at the limits
	ok := evalJSON(`{"name":"a","remember":[{"text":"0123456789"},{"text":"x"}],"query":"12345"}`, `{"name":"b","query":"q"}`)
	if got := problemsOf(ok, "e", l); got != "" {
		t.Errorf("at the limits: %s", got)
	}
	// zero limits mean no limit
	if got := problemsOf(evalJSON(`{"name":"a","query":"`+strings.Repeat("q", 9000)+`"}`), "e", Limits{}); got != "" {
		t.Errorf("zero limits: %s", got)
	}
}

func TestValidateUserReportsAllProblems(t *testing.T) {
	data := evalJSON(`{"name":"a b","remember":[{"text":"x","text_file":"../y"}],"chunk_size":1,"models":{"query":"nope"}}`)
	ps := ValidateUser(data, "e", testLimits())
	if len(ps) < 5 {
		t.Fatalf("only %d problems: %v", len(ps), ps)
	}
}

func TestValidTextFileAndName(t *testing.T) {
	for _, s := range []string{"a.txt", "corpus/doc.txt", "a/b/c-d_e.f.txt", "corpus/x.json"} {
		if !ValidTextFile(s) {
			t.Errorf("ValidTextFile(%q) = false", s)
		}
	}
	for _, s := range []string{"", "x.json", "/a", "a/", "a//b", "../a", "a/..", "..", ".", "a\\b", "a/.b"} {
		if ValidTextFile(s) {
			t.Errorf("ValidTextFile(%q) = true", s)
		}
	}
}

func TestTextFiles(t *testing.T) {
	ev, err := ParseUser(evalJSON(
		`{"name":"a","remember":[{"text_file":"z.txt"},{"text":"t"},{"text_file":"c/a.txt"}]}`,
		`{"name":"b","remember":[{"text_file":"z.txt"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := TextFiles(ev); !slices.Equal(got, []string{"c/a.txt", "z.txt"}) {
		t.Fatalf("TextFiles = %v", got)
	}
}
