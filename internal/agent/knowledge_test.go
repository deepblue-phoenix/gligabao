package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeKB records what the tools ask of the knowledge base.
type fakeKB struct {
	mu        sync.Mutex
	remembers []rememberCall
	queries   []string
}

type rememberCall struct {
	text string
	wait bool
}

func (f *fakeKB) Remember(_ context.Context, text string, wait bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remembers = append(f.remembers, rememberCall{text, wait})
	if wait {
		return "remembered", nil
	}
	return "queued", nil
}

func (f *fakeKB) Query(_ context.Context, q string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	return "kb answer", nil
}

func TestRememberToolIsAsyncByDefaultAndSyncOnRequest(t *testing.T) {
	kb := &fakeKB{}
	tool := rememberTool(kb)
	ctx := context.Background()

	out, err := tool.Run(ctx, json.RawMessage(`{"text":"  Acme was founded in 1999.  ","source":"https://acme.test/about"}`))
	if err != nil || out != "queued" {
		t.Fatalf("async: %q, %v", out, err)
	}
	out, err = tool.Run(ctx, json.RawMessage(`{"text":"Acme has 50 staff.","sync":true}`))
	if err != nil || out != "remembered" {
		t.Fatalf("sync: %q, %v", out, err)
	}
	want := []rememberCall{
		{"Source: https://acme.test/about\n\nAcme was founded in 1999.", false},
		{"Acme has 50 staff.", true},
	}
	if len(kb.remembers) != 2 || kb.remembers[0] != want[0] || kb.remembers[1] != want[1] {
		t.Fatalf("remember calls: %+v", kb.remembers)
	}
}

func TestRememberToolRejectsBadInput(t *testing.T) {
	tool := rememberTool(&fakeKB{})
	for name, in := range map[string]string{
		"no text":   `{"source":"u"}`,
		"blank":     `{"text":"  "}`,
		"not json":  `{`,
		"bad sync":  `{"text":"x","sync":"yes"}`,
		"too long":  `{"text":"` + strings.Repeat("a", maxRememberChars+1) + `"}`,
		"bad shape": `[]`,
	} {
		if _, err := tool.Run(context.Background(), json.RawMessage(in)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestQueryTool(t *testing.T) {
	kb := &fakeKB{}
	out, err := queryTool(kb).Run(context.Background(), json.RawMessage(`{"question":"who founded Acme?"}`))
	if err != nil || out != "kb answer" || len(kb.queries) != 1 || kb.queries[0] != "who founded Acme?" {
		t.Fatalf("got %q, %v, %v", out, err, kb.queries)
	}
	if _, err := queryTool(kb).Run(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty question accepted")
	}
}

func TestResearchAgentHasTheKnowledgeToolsOnlyWithAKnowledgeBase(t *testing.T) {
	names := func(a *Agent) string {
		var n []string
		for _, tool := range a.Tools {
			n = append(n, tool.Name)
		}
		return strings.Join(n, ",")
	}
	plain := ResearchAgent(nil, nil, nil)
	if got := names(plain); got != "web_search,fetch_url" || strings.Contains(plain.System, "remember") {
		t.Fatalf("without a KB: %s", got)
	}
	with := ResearchAgent(nil, nil, &fakeKB{})
	if got := names(with); got != "web_search,fetch_url,query,remember" {
		t.Fatalf("with a KB: %s", got)
	}
	for _, want := range []string{"query", "remember", "sync=false", "sync=true"} {
		if !strings.Contains(with.System, want) {
			t.Errorf("the prompt does not mention %q", want)
		}
	}
}

func TestKnowledgeErrorsAreReportedToTheModel(t *testing.T) {
	failing := &failingKB{}
	client, bodies := fakeAPI(t,
		`{"model":"m","content":[{"type":"tool_use","id":"t1","name":"query","input":{"question":"q"}}],"usage":{}}`,
		`{"model":"m","content":[{"type":"text","text":"ok"}],"usage":{}}`,
	)
	a := ResearchAgent(client, nil, failing)
	res, err := a.Run(context.Background(), "go")
	if err != nil || res.Answer != "ok" {
		t.Fatalf("got %+v, %v", res, err)
	}
	if !strings.Contains((*bodies)[1], "kb is down") || !strings.Contains((*bodies)[1], `"is_error":true`) {
		t.Fatalf("error not sent back: %s", (*bodies)[1])
	}
}

type failingKB struct{ fakeKB }

func (*failingKB) Query(context.Context, string) (string, error) { return "", errors.New("kb is down") }
