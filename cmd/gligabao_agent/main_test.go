package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
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

func setKeys(t *testing.T, anthropic, openai string) {
	t.Helper()
	t.Setenv(config.EnvAnthropicKey, anthropic)
	t.Setenv(config.EnvOpenAIKey, openai)
}

func TestUsageErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		stdin     string
		anthropic string
		openai    string
		args      []string
		want      string
	}{
		"no topic":         {anthropic: "k", args: nil, want: "exactly one topic"},
		"two topics":       {anthropic: "k", args: []string{"a", "b"}, want: "exactly one topic"},
		"empty stdin":      {anthropic: "k", args: []string{"-"}, stdin: "  \n", want: "empty topic"},
		"topic too long":   {anthropic: "k", args: []string{strings.Repeat("x", maxTopicChars+1)}, want: "too long"},
		"steps too small":  {anthropic: "k", args: []string{"--max-steps", "0", "t"}, want: "--max-steps"},
		"steps too big":    {anthropic: "k", args: []string{"--max-steps", "41", "t"}, want: "--max-steps"},
		"bad user":         {anthropic: "k", args: []string{"--user", " ", "t"}, want: "user_id"},
		"bad dataset":      {anthropic: "k", args: []string{"--dataset", "a b", "t"}, want: "dataset"},
		"no anthropic key": {args: []string{"t"}, want: config.EnvAnthropicKey},
		"no openai key":    {anthropic: "k", args: []string{"t"}, want: config.EnvOpenAIKey},
		"bad flag":         {anthropic: "k", args: []string{"--nope", "t"}, want: "not defined"},
		"missing config":   {anthropic: "k", args: []string{"--config", "/nonexistent/gligabao.toml", "t"}, want: "nonexistent"},
	} {
		t.Run(name, func(t *testing.T) {
			setKeys(t, tc.anthropic, tc.openai)
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
	code, stdout, _ := runArgs(t, "", "--version")
	if code != exitOK || !strings.HasPrefix(stdout, "gligabao_agent ") {
		t.Errorf("--version: code %d, stdout %q", code, stdout)
	}
	code, _, stderr := runArgs(t, "", "--help")
	if code != exitOK || !strings.Contains(stderr, config.EnvAnthropicKey) || !strings.Contains(stderr, "sub-agents") {
		t.Errorf("--help: code %d, stderr %q", code, stderr)
	}
}

// fakeAnthropic is a fake Messages API for the whole app: the agent that researches ROOT TOPIC remembers a snippet without waiting,
// spawns the sub-agent "kid" (task KID TASK), waits for it and writes ROOT REPORT; the sub-agent answers KID REPORT at once; the
// model calls of the remember pipeline extract nothing; the web search is not used.
func fakeAnthropic(t *testing.T) *ai.Client {
	t.Helper()
	const (
		toolUse = `{"model":"m","stop_reason":"tool_use","content":[%s],"usage":{"input_tokens":10,"output_tokens":5}}`
		text    = `{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"%s"}],"usage":{"input_tokens":20,"output_tokens":9}}`
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		switch {
		case !strings.Contains(body, `"name":"fetch_url"`): // not an agent: the remember pipeline
			io.WriteString(w, fmt.Sprintf(text, `{\"entities\":[],\"edges\":[]}`))
		case !strings.Contains(body, "ROOT TOPIC"): // the sub-agent
			io.WriteString(w, fmt.Sprintf(text, "KID REPORT"))
		default: // the root, by how many tool results it has seen
			switch strings.Count(body, `"tool_use_id"`) {
			case 0:
				io.WriteString(w, fmt.Sprintf(toolUse, `{"type":"tool_use","id":"r1","name":"remember","input":{"text":"Acme was founded in 1999.","source":"https://acme.test"}},`+
					`{"type":"tool_use","id":"s1","name":"spawn_subagent","input":{"name":"kid","reason":"a part of its own","task":"KID TASK"}}`))
			case 2:
				io.WriteString(w, fmt.Sprintf(toolUse, `{"type":"tool_use","id":"w1","name":"wait_subagents","input":{}}`))
			default:
				io.WriteString(w, fmt.Sprintf(text, "ROOT REPORT"))
			}
		}
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

// The whole app over a SQLite file and a chromem directory: the research manager runs the agent, which remembers into the knowledge
// base (without waiting: the remember run must be over when the research returns) and spawns a sub-agent; every run is recorded.
func TestResearchWithKnowledgeBaseAndSubagents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "agent.db")
	cfg := config.Default()
	cfg.EmbeddingDimensions = 2
	a, err := openApp(ctx, cfg, fakeAnthropic(t), fakeEmbedder{}, dbFile, "")
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	out, err := a.runResearch(ctx, "u1", agent_research.Options{Topic: "ROOT TOPIC", MaxSteps: 6}, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if out.DrainErr != nil {
		t.Errorf("drain: %v", out.DrainErr)
	}
	if out.Root.Status != core.StatusCompleted || out.Root.Report != "ROOT REPORT" {
		t.Errorf("root: %s %q (%s)", out.Root.Status, out.Root.Report, out.Root.Error)
	}
	if len(out.Kids) != 1 || out.Kids[0].Name != "kid" || out.Kids[0].Report != "KID REPORT" || out.Kids[0].Status != core.StatusCompleted {
		t.Errorf("sub-agents: %+v", out.Kids)
	}
	if steps, _, _, _, cost := out.totals(); steps != out.Root.Steps+out.Kids[0].Steps || out.Kids[0].Steps == 0 || cost != out.Root.CostUSD+out.Kids[0].CostUSD {
		t.Errorf("totals do not sum the tree: steps %d, cost %v (root %d, kid %d)", steps, cost, out.Root.Steps, out.Kids[0].Steps)
	}
	for _, want := range []string{"[agent] remembering", "[agent] spawning: kid", "[sub-agent kid] thinking"} {
		if !strings.Contains(progress.String(), want) {
			t.Errorf("progress lacks %q:\n%s", want, progress.String())
		}
	}
	// The remember run that did not wait has ended (not left started), and was done in the user's dataset.
	runs, total, err := relational.ListRememberRuns(ctx, a.database.Relational, relational.RememberRunFilter{}, 10, 0)
	if err != nil || total != 1 || len(runs) != 1 || runs[0].Status != relational.RememberSucceeded {
		t.Errorf("remember runs: %+v, %v", runs, err)
	}
	a.close()

	for _, p := range []string{dbFile, filepath.Join(dir, "agent.vectors")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
}

// A research that is cancelled ends as a failed run, and the binary is told which one.
func TestResearchInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := config.Default()
	cfg.EmbeddingDimensions = 2
	a, err := openApp(ctx, cfg, fakeAnthropic(t), fakeEmbedder{}, filepath.Join(t.TempDir(), "agent.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	cancel()
	if _, err := a.runResearch(ctx, "u1", agent_research.Options{Topic: "ROOT TOPIC"}, nil); err == nil {
		t.Error("starting a research with a done context should fail")
	}
}

// The point of this binary is to be small: it has the knowledge base but must not link the server, the HTTP API, training or evals.
func TestMinimalDependencies(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	const prefix = "github.com/deepblue-phoenix/gligabao/"
	forbidden := []string{
		"internal/api", "internal/sys/server", "internal/sys/sidecar", "internal/training", "internal/training_audits", "internal/evals",
		"internal/db/dbfull", "cli", "monitoring", "admin_ui",
	}
	for _, pkg := range strings.Fields(string(out)) {
		rest, ok := strings.CutPrefix(pkg, prefix)
		if !ok {
			continue
		}
		for _, f := range forbidden {
			if rest == f || strings.HasPrefix(rest, f+"/") {
				t.Errorf("gligabao_agent must not depend on %s (keep the binary minimal)", pkg)
			}
		}
	}
}
