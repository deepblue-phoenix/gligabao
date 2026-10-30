package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run runs the CLI against a fake server (--server) and returns what it printed and the error.
func run(t *testing.T, h http.HandlerFunc, stdin string, args ...string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(Options{})
	var out strings.Builder
	c.Root.SetOut(&out)
	c.Root.SetIn(strings.NewReader(stdin))
	c.Root.SetArgs(append([]string{"--server", srv.URL}, args...))
	err := c.Root.Execute()
	return out.String(), err
}

func TestRememberBody(t *testing.T) {
	var got map[string]any
	out, err := run(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/gb/remember" {
			t.Errorf("%s %s", r.Method, r.URL)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"artifact_id":1}`))
	}, "", "remember", "Alice works at Acme.", "-d", "work", "--kind", "episode", "--session", "s1", "--model", "extract_graph=claude-x", "--web-search")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"artifact_id": 1`) {
		t.Errorf("out = %q", out)
	}
	if got["user_id"] != "local_user" || got["text"] != "Alice works at Acme." || got["dataset"] != "work" || got["kind"] != "episode" ||
		got["session_id"] != "s1" || got["web_search"] != true {
		t.Errorf("body = %v", got)
	}
	if m, _ := got["models"].(map[string]any); m["extract_graph"] != "claude-x" {
		t.Errorf("models = %v", got["models"])
	}
}

func TestRememberFromStdinAndJSON(t *testing.T) {
	var got map[string]any
	h := func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{}`))
	}
	if _, err := run(t, h, "from stdin", "remember", "-"); err != nil || got["text"] != "from stdin" {
		t.Errorf("stdin: %v %v", err, got)
	}
	// The flags override the fields of --json, and user_id is only the default.
	got = nil
	if _, err := run(t, h, "", "--user", "u9", "remember", "--json", `{"text":"t","user_id":"u1","dataset":"a"}`, "-d", "b"); err != nil {
		t.Fatal(err)
	}
	if got["user_id"] != "u1" || got["dataset"] != "b" || got["text"] != "t" {
		t.Errorf("body = %v", got)
	}
	// A file.
	f := filepath.Join(t.TempDir(), "n.txt")
	os.WriteFile(f, []byte("from file"), 0o644)
	got = nil
	if _, err := run(t, h, "", "remember", "-f", f); err != nil || got["text"] != "from file" {
		t.Errorf("file: %v %v", err, got)
	}
}

func TestRememberNothing(t *testing.T) {
	_, err := run(t, func(http.ResponseWriter, *http.Request) { t.Error("called") }, "", "remember")
	if codeOf(err) != exitUsage {
		t.Errorf("err = %v, code %d", err, codeOf(err))
	}
}

func TestQueryEvidenceOnlyAndRaw(t *testing.T) {
	var got map[string]any
	out, err := run(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"answer":"Acme.","evidence":{}}`))
	}, "", "query", "Where?", "--evidence-only", "--geo-radius", "80", "--raw")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Acme.\n" {
		t.Errorf("raw out = %q", out)
	}
	if got["query"] != "Where?" || got["evidence_only"] != true || got["geo_radius_km"] != 80.0 {
		t.Errorf("body = %v", got)
	}
}

func TestExitCodes(t *testing.T) {
	for status, want := range map[int]int{400: exitUsage, 404: exitNotFound, 409: exitConflict, 503: exitUnavailable, 502: exitUnavailable} {
		_, err := run(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":"nope"}`)
		}, "", "dataset", "ls")
		if codeOf(err) != want || err.Error() != "nope" {
			t.Errorf("status %d: err %v code %d, want %d", status, err, codeOf(err), want)
		}
	}
}

func TestDatasetRequests(t *testing.T) {
	var seen []string
	h := func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		w.Write([]byte(`{}`))
	}
	for _, args := range [][]string{{"dataset", "ls"}, {"dataset", "rm", "w"}, {"dataset", "graph", "w"}} {
		if _, err := run(t, h, "", args...); err != nil {
			t.Fatal(err)
		}
	}
	want := "GET /v1/gb/datasets?user_id=local_user|DELETE /v1/gb/datasets/w?user_id=local_user|GET /v1/gb/datasets/w/graph?user_id=local_user"
	if strings.Join(seen, "|") != want {
		t.Errorf("requests = %v", seen)
	}
}

func TestResolveLayout(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv(EnvHome, home)
	t.Chdir(cwd)

	l, err := resolveLayout("/x/gligabao.toml", false)
	if err != nil || l.Project || l.ConfigPath != "" || l.DBPath != filepath.Join(home, "gligabao.db") {
		t.Errorf("no config in cwd: %+v %v", l, err)
	}
	os.WriteFile(filepath.Join(home, "gligabao.toml"), []byte("port = 9999\n"), 0o644)
	if l, _ = resolveLayout("", false); l.ConfigPath != filepath.Join(home, "gligabao.toml") {
		t.Errorf("home config: %+v", l)
	}
	os.WriteFile(filepath.Join(cwd, "gligabao.toml"), []byte("port = 9998\n"), 0o644)
	if l, _ = resolveLayout("", false); !l.Project || l.ConfigPath != "gligabao.toml" || l.DBPath != "" {
		t.Errorf("config in cwd: %+v", l)
	}
	if l, _ = resolveLayout("/etc/g.toml", true); !l.Project || l.ConfigPath != "/etc/g.toml" {
		t.Errorf("--config: %+v", l)
	}
}

func TestDaemonArgs(t *testing.T) {
	d := &daemon{layout: layout{ConfigPath: "c.toml", DBPath: "/h/g.db"}}
	if got := strings.Join(d.args(), " "); got != "--config c.toml serve --host 127.0.0.1 --db /h/g.db" {
		t.Errorf("args = %q", got)
	}
}
