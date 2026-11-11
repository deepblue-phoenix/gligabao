package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/dbexport"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	log "github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	log.SetLevel(log.WarnLevel)
	os.Exit(m.Run())
}

func runArgs(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsageErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		stdin     string
		anthropic string
		openai    string
		args      []string
		want      string
	}{
		"no command":          {args: nil, want: "COMMAND"},
		"unknown command":     {args: []string{"nope"}, want: "unknown command"},
		"remember no text":    {anthropic: "k", args: []string{"remember"}, want: "exactly one text"},
		"query two args":      {anthropic: "k", args: []string{"query", "a", "b"}, want: "exactly one question"},
		"bad user":            {anthropic: "k", args: []string{"query", "--user", " ", "q"}, want: "user_id"},
		"bad dataset":         {anthropic: "k", args: []string{"query", "--dataset", "a b", "q"}, want: "dataset"},
		"bad time":            {anthropic: "k", args: []string{"remember", "--at", "yesterday", "t"}, want: "--at"},
		"no anthropic key":    {args: []string{"query", "q"}, want: config.EnvAnthropicKey},
		"no openai key":       {anthropic: "k", args: []string{"query", "q"}, want: config.EnvOpenAIKey},
		"flag of another cmd": {anthropic: "k", openai: "k", args: []string{"query", "--kind", "episode", "q"}, want: "not defined"},
		"missing config":      {anthropic: "k", args: []string{"query", "--config", "/nonexistent/gligabao.toml", "q"}, want: "nonexistent"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(config.EnvAnthropicKey, tc.anthropic)
			t.Setenv(config.EnvOpenAIKey, tc.openai)
			t.Chdir(t.TempDir()) // the default database must not be created in the source tree
			code, stdout, stderr := runArgs(t, tc.stdin, tc.args...)
			if code != exitUsage {
				t.Errorf("exit code %d, want %d (stderr: %s)", code, exitUsage, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout should be empty, got %q", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr %q does not contain %q", stderr, tc.want)
			}
		})
	}
}

func TestVersionAndHelp(t *testing.T) {
	code, stdout, _ := runArgs(t, "", "version")
	if code != exitOK || !strings.HasPrefix(stdout, "gligabao_pipelines ") {
		t.Errorf("version: code %d, stdout %q", code, stdout)
	}
	code, stdout, _ = runArgs(t, "", "--help")
	if code != exitOK || !strings.Contains(stdout, "remember") {
		t.Errorf("--help: code %d, stdout %q", code, stdout)
	}
}

// fakeAnthropic is a fake Messages API: every model call answers with the extraction of one entity and no facts, which is also the text
// the query "answers" with.
func fakeAnthropic(t *testing.T) *ai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, `{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"{\"entities\":[],\"edges\":[]}"}],"usage":{"input_tokens":20,"output_tokens":9}}`)
	}))
	t.Cleanup(srv.Close)
	c, err := ai.New("k", ai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type fakeEmbedder struct{}

func (fakeEmbedder) EmbedBatchUsage(_ context.Context, texts []string) ([][]float32, *ai.EmbedUsage, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, &ai.EmbedUsage{Provider: ai.ProviderOpenAI, Model: "test-embed"}, nil
}

func open(t *testing.T, dbFile string) *app {
	t.Helper()
	cfg := config.Default()
	cfg.EmbeddingDimensions = 2
	a, err := openApp(context.Background(), cfg, fakeAnthropic(t), fakeEmbedder{}, dbFile, "")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// The life of a Lambda: the first process remembers and exits; a second one, over the same files, finds what was remembered. A directory
// of the database that does not exist yet is created.
func TestRememberThenQueryInAnotherProcess(t *testing.T) {
	ctx := context.Background()
	dbFile := filepath.Join(t.TempDir(), "state", "gligabao.db")

	a := open(t, dbFile)
	var out, errb bytes.Buffer
	req := memory_remember.Request{UserID: "u1", Dataset: "main", Text: "Acme was founded in 1999.", Opts: a.rememberOpts}
	if code := a.cmdRemember(ctx, &out, &errb, false, req); code != exitOK {
		t.Fatalf("remember: code %d, stderr %s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "remembered (run ") {
		t.Errorf("remember output: %q", out.String())
	}
	a.close()
	for _, p := range []string{dbFile, strings.TrimSuffix(dbFile, ".db") + ".vectors"} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	b := open(t, dbFile)
	out.Reset()
	errb.Reset()
	if code := b.cmdQuery(ctx, &out, &errb, true, "u1", "main", "When was Acme founded?", nil); code != exitOK {
		t.Fatalf("query: code %d, stderr %s", code, errb.String())
	}
	var res map[string]any
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res["answer"] == nil {
		t.Errorf("query --json output: %q, %v", out.String(), err)
	}
	// The text is in the second process's database: the artifact is there, not only the run.
	var n int
	if err := b.database.Relational.QueryRow(`SELECT COUNT(*) FROM gb_artifacts WHERE text LIKE '%founded in 1999%'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("artifacts with the text: %d, %v", n, err)
	}

	// A user who never remembered anything gets an empty knowledge base, not an error.
	out.Reset()
	if code := b.cmdQuery(ctx, &out, &errb, false, "nobody", "main", "anything?", nil); code != exitOK || !strings.Contains(out.String(), "empty") {
		t.Errorf("query of an empty knowledge base: code %d, %q", code, out.String())
	}

	// The export has what the first process remembered.
	archive := filepath.Join(t.TempDir(), "x.zip")
	m, err := b.fullExport(ctx, archive)
	if err != nil || m.Source != "gligabao_pipelines" {
		t.Fatalf("export: %+v, %v", m, err)
	}
	r, err := dbexport.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var texts []string
	r.Rows("gb_artifacts", func(row dbexport.Row) error { texts = append(texts, fmt.Sprint(row["text"])); return nil })
	if len(texts) != 1 || !strings.Contains(texts[0], "Acme was founded in 1999.") {
		t.Errorf("artifacts in the export: %q", texts)
	}
	b.close()
}

// A remember that fails (its context is done) is reported with exit code 1 and nothing is claimed as remembered.
func TestRememberCancelled(t *testing.T) {
	a := open(t, filepath.Join(t.TempDir(), "g.db"))
	defer a.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errb bytes.Buffer
	code := a.cmdRemember(ctx, &out, &errb, false, memory_remember.Request{UserID: "u1", Dataset: "main", Text: "x", Opts: a.rememberOpts})
	if code != exitError || out.Len() != 0 {
		t.Errorf("code %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
}

// The binary stays small: none of what the server has may get linked.
func TestMinimalDependencies(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	const prefix = "github.com/deepblue-phoenix/gligabao/"
	forbidden := []string{
		"internal/api", "internal/agent_research", "internal/sys/server", "internal/sys/sidecar", "internal/training", "internal/training_audits", "internal/evals",
		"internal/db/dbfull", "internal/db/dbimport", "cli", "monitoring", "admin_ui",
	}
	for _, pkg := range strings.Fields(string(out)) {
		rest, ok := strings.CutPrefix(pkg, prefix)
		if !ok {
			continue
		}
		for _, f := range forbidden {
			if rest == f || strings.HasPrefix(rest, f+"/") {
				t.Errorf("gligabao_pipelines must not depend on %s (keep the binary minimal)", pkg)
			}
		}
	}
}
