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

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/dbexport"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	log "github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	log.SetLevel(log.WarnLevel)
	os.Exit(m.Run())
}

func runArgs(t *testing.T,
	stdin string,
	args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(context.Background(),
		args,
		strings.NewReader(stdin),
		&out,
		&errb)
	return code, out.String(), errb.String()
}

func setKeys(t *testing.T,
	anthropic,
	openai string) {
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
		"no topic": {
			anthropic: "k",
			args:      nil,
			want:      "exactly one topic",
		},
		"two topics": {
			anthropic: "k",
			args:      []string{"a", "b"},
			want:      "exactly one topic",
		},
		"empty stdin": {
			anthropic: "k",
			args:      []string{"-"},
			stdin:     "  \n",
			want:      "empty topic",
		},
		"topic too long": {
			anthropic: "k",
			args:      []string{strings.Repeat("x", maxTopicChars+1)},
			want:      "too long",
		},
		"steps too small": {
			anthropic: "k",
			args:      []string{"--max-steps", "-1", "t"},
			want:      "--max-steps",
		},
		"negative cost cap": {
			anthropic: "k",
			args:      []string{"--max-cost-usd", "-1", "t"},
			want:      "--max-cost-usd",
		},
		"bundle and dir": {
			anthropic: "k",
			args:      []string{"--agent-bundle", "b.json", "--agent-defs-dir", "d", "t"},
			want:      "use one",
		},
		"unknown agent": {
			anthropic: "k",
			args:      []string{"--agent", "nope", "--agent-defs-dir", t.TempDir(), "t"},
			want:      "unknown agent",
		},
		"steps too big": {
			anthropic: "k",
			args:      []string{"--max-steps", "41", "t"},
			want:      "--max-steps",
		},
		"bad user": {
			anthropic: "k",
			args:      []string{"--user", " ", "t"},
			want:      "user_id",
		},
		"bad dataset": {
			anthropic: "k",
			args:      []string{"--dataset", "a b", "t"},
			want:      "dataset",
		},
		"no anthropic key": {
			args: []string{"t"},
			want: config.EnvAnthropicKey,
		},
		"no openai key": {
			anthropic: "k",
			args:      []string{"t"},
			want:      config.EnvOpenAIKey,
		},
		"atif without json": {
			anthropic: "k",
			args:      []string{"--atif", "t"},
			want:      "--json",
		},
		"events and verbose": {
			anthropic: "k",
			args:      []string{"--events", "--verbose", "t"},
			want:      "use one",
		},
		"bad flag": {
			anthropic: "k",
			args:      []string{"--nope", "t"},
			want:      "not defined",
		},
		"missing config": {
			anthropic: "k",
			args:      []string{"--config", "/nonexistent/gligabao.toml", "t"},
			want:      "nonexistent",
		},
	} {
		t.Run(name, func(t *testing.T) {
			setKeys(t,
				tc.anthropic,
				tc.openai)
			t.Chdir(t.TempDir()) // the default database must not be created in the source tree
			code, stdout, stderr := runArgs(t,
				tc.stdin,
				tc.args...)
			if code != exitUsage {
				t.Errorf("exit code %d, want %d (stderr: %s)",
					code,
					exitUsage,
					stderr)
			}
			if stdout != "" {
				t.Errorf("stdout should be empty, got %q", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr %q does not contain %q",
					stderr,
					tc.want)
			}
		})
	}
}

func TestVersionAndHelp(t *testing.T) {
	code, stdout, _ := runArgs(t,
		"",
		"--version")
	if code != exitOK || !strings.HasPrefix(stdout, "gligabao_agent ") {
		t.Errorf("--version: code %d, stdout %q",
			code,
			stdout)
	}
	code, _, stderr := runArgs(t,
		"",
		"--help")
	if code != exitOK || !strings.Contains(stderr, config.EnvAnthropicKey) || !strings.Contains(stderr, "knowledge base") {
		t.Errorf("--help: code %d, stderr %q",
			code,
			stderr)
	}
}

// fakeAnthropic is a fake Messages API for the whole app: the agent that researches ROOT TOPIC remembers a snippet without waiting and
// writes ROOT REPORT; the model calls of the remember pipeline extract nothing; the web search is not used.
func fakeAnthropic(t *testing.T) *llm.Client {
	t.Helper()
	const (
		toolUse = `{"model":"m","stop_reason":"tool_use","content":[%s],"usage":{"input_tokens":10,"output_tokens":5}}`
		text    = `{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"%s"}],"usage":{"input_tokens":20,"output_tokens":9}}`
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		switch {
		case !strings.Contains(body, `"name":"fetch_url"`): // not an agent: the remember pipeline
			io.WriteString(w, fmt.Sprintf(text, `{\"entities\":[],\"edges\":[]}`))
		default: // the agent, by how many tool results it has seen
			switch strings.Count(body, `"tool_use_id"`) {
			case 0:
				io.WriteString(w, fmt.Sprintf(toolUse, `{"type":"tool_use","id":"r1","name":"remember","input":{"text":"Acme was founded in 1999.","source":"https://acme.test"}}`))
			default:
				io.WriteString(w, fmt.Sprintf(text, "ROOT REPORT"))
			}
		}
	}))
	t.Cleanup(srv.Close)
	c, err := llm.New("k", llm.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type fakeEmbedder struct{}

func (fakeEmbedder) EmbedBatchUsage(_ context.Context,
	texts []string) ([][]float32, *llm.EmbedUsage, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, &llm.EmbedUsage{
		Provider: llm.ProviderOpenAI,
		Model:    "test-embed",
	}, nil
}

// The whole app over a SQLite file and a chromem directory: the research manager runs the agent, which remembers into the knowledge
// base (without waiting: the remember run must be over when the research returns); every run is recorded.
func TestResearchWithKnowledgeBase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "agent.db")
	cfg := config.Default()
	cfg.EmbeddingDimensions = 2
	a, err := openApp(ctx,
		cfg,
		fakeAnthropic(t),
		fakeEmbedder{},
		dbFile,
		"")
	if err != nil {
		t.Fatal(err)
	}
	var progress, events bytes.Buffer
	trajectory := a.captureTrajectory()
	text := textProgress(&progress)
	jsonState, jsonUsage := jsonEvents(&events)
	out, err := a.runResearch(ctx,
		"u1",
		agent_research.Options{
			Topic:    "ROOT TOPIC",
			MaxSteps: 6,
		},
		func(ev stateEvent) { text(ev); jsonState(ev) },
		jsonUsage)
	if err != nil {
		t.Fatal(err)
	}
	if out.DrainErr != nil {
		t.Errorf("drain: %v", out.DrainErr)
	}
	if out.Root.Status != core.StatusCompleted || out.Root.Report != "ROOT REPORT" {
		t.Errorf("root: %s %q (%s)",
			out.Root.Status,
			out.Root.Report,
			out.Root.Error)
	}
	for _, want := range []string{"[agent] remembering"} {
		if !strings.Contains(progress.String(), want) {
			t.Errorf("progress lacks %q:\n%s",
				want,
				progress.String())
		}
	}
	// The same progress as JSON lines (--events), which the Modal runner reads: the states of both agents, then the totals of the tree.
	var states, usages int
	var lastUsage map[string]any
	for _, line := range strings.Split(strings.TrimSpace(events.String()), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not a JSON line: %q", line)
		}
		switch ev["event"] {
		case "state":
			states++
			if ev["agent"] == "main" && ev["state"] == "thinking" && ev["state_seq"] == nil {
				t.Errorf("a state event has no state_seq: %v", ev)
			}
		case "usage":
			usages++
			lastUsage = ev
		}
	}
	if states == 0 || usages == 0 || lastUsage["subagents"] != float64(0) || lastUsage["steps"] != float64(out.Root.Steps) {
		t.Errorf("events: %d states, %d usages, last usage %v\n%s",
			states,
			usages,
			lastUsage,
			events.String())
	}
	for _, want := range []string{`"agent":"main"`, `"state":"remembering"`} {
		if !strings.Contains(events.String(), want) {
			t.Errorf("events lack %s:\n%s",
				want,
				events.String())
		}
	}
	// The trajectory of the research.
	traj := trajectory()
	if traj == nil {
		t.Fatal("no trajectory")
	}
	perAgent := map[any]int{}
	for _, st := range traj.Steps {
		perAgent[st.Extra["agent"]]++
	}
	// system, user and 2 model calls for the main agent.
	if perAgent["main"] != 4 || len(traj.Steps) != 4 {
		t.Errorf("steps per agent: %v", perAgent)
	}
	// The remember run that did not wait has ended (not left started), and was done in the user's dataset.
	runs, total, err := relational.ListRememberRuns(ctx,
		a.database.Relational,
		relational.RememberRunFilter{},
		10,
		0)
	if err != nil || total != 1 || len(runs) != 1 || runs[0].Status != relational.RememberSucceeded {
		t.Errorf("remember runs: %+v, %v",
			runs,
			err)
	}

	// The full export has all of it: the knowledge base, the runs of both agents with their states, the vectors and the trajectory.
	if err := a.database.Vector.Upsert(ctx,
		"dataset-1-entities",
		[]vector.Vector{{
			ID:       "entity:1",
			Values:   []float32{1, 0},
			Metadata: map[string]any{"kind": "entity"},
		}}); err != nil {
		t.Fatal(err)
	}
	trajJSON, _ := atif.Marshal(traj)
	archive := filepath.Join(dir,
		"out",
		"agent-export.zip")
	m, err := a.fullExport(ctx,
		archive,
		dbexport.Extra{
			Name: trajectoryFile,
			Data: trajJSON,
		})
	if err != nil {
		t.Fatal(err)
	}
	r, err := dbexport.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	rows := map[string]int64{}
	for _, tb := range m.Tables {
		rows[tb.Name] = tb.Rows
	}
	if rows["gb_agent_research_runs"] != 1 || rows["gb_agent_research_states"] == 0 || rows["gb_artifacts"] == 0 || rows["gb_remember_runs"] != 1 || rows["gb_inferences"] == 0 {
		t.Errorf("rows per table: %v", rows)
	}
	var reports []string
	if err := r.Rows("gb_agent_research_runs", func(row dbexport.Row) error {
		reports = append(reports, fmt.Sprint(row["report"]))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(reports, "|") != "ROOT REPORT" {
		t.Errorf("reports of the runs: %q", reports)
	}
	var texts []string
	r.Rows("gb_artifacts", func(row dbexport.Row) error { texts = append(texts, fmt.Sprint(row["text"])); return nil })
	if len(texts) == 0 || !strings.Contains(texts[0], "Acme was founded in 1999.") {
		t.Errorf("the remembered text is not in the export: %q", texts)
	}
	var vecs int
	r.Vectors("dataset-1-entities", func(vector.Vector) error { vecs++; return nil })
	if vecs != 1 || m.EmbeddingDimensions != 2 || m.Backend != "sqlite" || m.Source != "gligabao_agent" {
		t.Errorf("vectors %d, manifest %+v",
			vecs,
			m)
	}
	if b, err := r.Extra(trajectoryFile); err != nil || !strings.Contains(string(b), "ROOT TOPIC") {
		t.Errorf("trajectory: %q, %v",
			b,
			err)
	}
	a.close()

	for _, p := range []string{dbFile, filepath.Join(dir, "agent.vectors")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v",
				p,
				err)
		}
	}
}

// A research that is cancelled ends as a failed run, and the binary is told which one.
func TestResearchInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := config.Default()
	cfg.EmbeddingDimensions = 2
	a, err := openApp(ctx,
		cfg,
		fakeAnthropic(t),
		fakeEmbedder{},
		filepath.Join(t.TempDir(), "agent.db"),
		"")
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	cancel()
	if _, err := a.runResearch(ctx,
		"u1",
		agent_research.Options{
			Topic: "ROOT TOPIC",
		},
		nil,
		nil); err == nil {
		t.Error("starting a research with a done context should fail")
	}
}

// The point of this binary is to be small: it has the knowledge base but must not link the server, the HTTP API, training or evals.
func TestMinimalDependencies(t *testing.T) {
	out, err := exec.Command("go",
		"list",
		"-deps",
		".").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	const prefix = "github.com/deepblue-phoenix/gligabao/"
	forbidden := []string{
		"internal/api", "internal/sys/server", "internal/sys/sidecar", "internal/training", "internal/training_audits", "internal/evals",
		"internal/db/dbfull", "internal/db/dbimport", "cli", "monitoring", "admin_ui",
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
