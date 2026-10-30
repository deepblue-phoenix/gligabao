package agent_research_test

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/vector"
	"github.com/deepblue-phoenix/gligabao/internal/memory"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// kbLLM is the LLM of the knowledge base: it extracts nothing, so a query finds nothing and answers without calling it.
type kbLLM struct{ calls atomic.Int32 }

func (l *kbLLM) Complete(_ context.Context, _ ai.Request) (*ai.Response, error) {
	l.calls.Add(1)
	return &ai.Response{Text: `{"entities":[],"edges":[]}`, StopReason: "end_turn", Model: "fake-model"}, nil
}

// kbOptions are the options of the remember and query calls: a vector store in memory and an embedder, like the server has.
func kbOptions(t *testing.T) []memory_core.Option {
	t.Helper()
	vs, err := vector.NewChromem(":memory:", 2)
	if err != nil {
		t.Fatal(err)
	}
	return []memory_core.Option{memory.WithVectors(vs, fakeEmbedder{})}
}

type fakeEmbedder struct{}

func (fakeEmbedder) EmbedBatchUsage(_ context.Context, texts []string) ([][]float32, *ai.EmbedUsage, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, &ai.EmbedUsage{Provider: ai.ProviderOpenAI, Model: "test-embed"}, nil
}

func toolUse(id, name, input string) string {
	return `{"model":"m","stop_reason":"tool_use","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}],"usage":{"input_tokens":1,"output_tokens":1}}`
}

func TestResearchWorksWithTheKnowledgeBase(t *testing.T) { runOnBackends(t, testKnowledge) }

// The agent remembers a snippet without waiting, another one waiting, and queries the knowledge base: the remember runs are the user's,
// in the dataset the run asked for, and the tools show as states of the run.
func testKnowledge(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	llm := &kbLLM{}
	mgr := memory_remember.NewManager(ctx, d, llm)
	fake := &fakeAnthropic{main: []string{
		toolUse("t1", "remember", `{"text":"Acme was founded in 1999.","source":"https://acme.test/about"}`),
		toolUse("t2", "remember", `{"text":"Acme has 50 staff.","sync":true}`),
		toolUse("t3", "query", `{"question":"when was Acme founded?"}`),
		finalReport,
	}}
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	opts := kbOptions(t)
	m.UseMemory(&agent_research.Memory{Remember: mgr, DB: d, LLM: llm, RememberOpts: opts, QueryOpts: opts})

	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "Acme", Dataset: "research"})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	run, err := m.Get(ctx, "u1", started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != core.StatusCompleted || run.ToolCalls != 3 {
		t.Fatalf("run: %+v", run)
	}
	if got := stateNames(run.States); got != "starting,thinking,remembering,thinking,remembering,thinking,querying,thinking,completed" {
		t.Fatalf("states: %s", got)
	}
	if s := run.States[2]; !strings.HasPrefix(s.Detail, "async: Acme was founded") || s.Error != "" {
		t.Errorf("async remember: %+v", s)
	}
	if s := run.States[4]; !strings.HasPrefix(s.Detail, "sync: Acme has 50") || s.Error != "" {
		t.Errorf("sync remember: %+v", s)
	}
	if s := run.States[6]; s.Detail != "when was Acme founded?" || s.Error != "" {
		t.Errorf("query: %+v", s)
	}

	// The first request offers the tools and the prompt that explains them; the later ones carry what the tools returned.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if first := fake.bodies[0]; !strings.Contains(first, `"name":"remember"`) || !strings.Contains(first, `"name":"query"`) || !strings.Contains(first, "knowledge base") {
		t.Errorf("first request: %s", first)
	}
	if !strings.Contains(fake.bodies[1], "queued (remember run") {
		t.Errorf("async result: %s", fake.bodies[1])
	}
	if !strings.Contains(fake.bodies[2], "remembered (remember run") {
		t.Errorf("sync result: %s", fake.bodies[2])
	}
	if !strings.Contains(fake.bodies[3], "I don't have any information in memory") { // the answer of an empty graph
		t.Errorf("query result: %s", fake.bodies[3])
	}

	// Both texts are in the dataset of the user (the sync one ran after the async one: the manager runs one at a time).
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM gb_remember_runs WHERE user_id = 'u1' AND dataset = 'research' AND status = 'succeeded'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("remember runs: %d, %v", n, err)
	}
}

func TestResearchWithoutAKnowledgeBaseHasOnlyTheWebTools(t *testing.T) {
	runOnBackends(t, testNoKnowledge)
}

func testNoKnowledge(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := &fakeAnthropic{main: []string{finalReport}}
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	if _, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t"}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if b := fake.bodies[0]; strings.Contains(b, `"name":"remember"`) || strings.Contains(b, `"name":"query"`) || !strings.Contains(b, `"name":"web_search"`) {
		t.Errorf("tools: %s", b)
	}
}

func TestQueryOfAnEmptyKnowledgeBaseIsNotAnError(t *testing.T) { runOnBackends(t, testEmptyKnowledge) }

func testEmptyKnowledge(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	llm := &kbLLM{}
	fake := &fakeAnthropic{main: []string{toolUse("t1", "query", `{"question":"anything?"}`), finalReport}}
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	opts := kbOptions(t)
	m.UseMemory(&agent_research.Memory{Remember: memory_remember.NewManager(ctx, d, llm), DB: d, LLM: llm, RememberOpts: opts, QueryOpts: opts})
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	run, _ := m.Get(ctx, "u1", started.ID)
	if run.Status != core.StatusCompleted || run.States[2].Error != "" || llm.calls.Load() != 0 {
		t.Fatalf("run: %+v states %s llm calls %d", run, stateNames(run.States), llm.calls.Load())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !strings.Contains(fake.bodies[1], "The knowledge base is empty") {
		t.Errorf("result: %s", fake.bodies[1])
	}
}

func TestStartRejectsABadDataset(t *testing.T) { runOnBackends(t, testBadDataset) }

func testBadDataset(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	m := agent_research.NewManager(ctx, d, ops, (&fakeAnthropic{main: []string{finalReport}}).start(t))
	if _, err := m.Start(ctx, "u1", agent_research.Options{Topic: "t", Dataset: "no spaces/allowed"}); !isKind(err, agent_research.KindInvalid) {
		t.Fatalf("bad dataset: %v", err)
	}
}
