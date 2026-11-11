package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
)

// fakeAPI answers with the given responses, in order, and records the request bodies.
func fakeAPI(t *testing.T, responses ...string) (*ai.Client, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Write([]byte(responses[len(bodies)-1]))
	}))
	t.Cleanup(srv.Close)
	c, err := ai.New("k", ai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c, &bodies
}

func TestRunCallsToolThenAnswers(t *testing.T) {
	client, bodies := fakeAPI(t,
		`{"model":"m","stop_reason":"tool_use","content":[{"type":"text","text":"let me check"},{"type":"tool_use","id":"t1","name":"echo","input":{"query":"hi"}}],"usage":{"input_tokens":1,"output_tokens":2}}`,
		`{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":3,"output_tokens":4}}`,
	)
	echo := Tool{Name: "echo", Description: "echo", Schema: objectSchema("query", "q"),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			q, err := stringArg(input, "query")
			return "echo:" + q, err
		}}
	res, err := (&Agent{Client: client, Tools: []Tool{echo}}).Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "done" || res.Steps != 2 || res.ToolCalls != 1 || res.InputTokens != 4 || res.OutputTokens != 6 {
		t.Fatalf("bad result: %+v", res)
	}
	// The second request carries the model's tool_use and our tool_result.
	if second := (*bodies)[1]; !strings.Contains(second, `"tool_use_id":"t1"`) || !strings.Contains(second, "echo:hi") {
		t.Fatalf("tool result not sent back: %s", second)
	}
}

func TestRunUnknownToolIsReportedToTheModel(t *testing.T) {
	client, bodies := fakeAPI(t,
		`{"model":"m","content":[{"type":"tool_use","id":"t1","name":"nope","input":{}}],"usage":{}}`,
		`{"model":"m","content":[{"type":"text","text":"ok"}],"usage":{}}`,
	)
	res, err := (&Agent{Client: client, Tools: []Tool{{Name: "x", Schema: objectSchema("a", "a")}}}).Run(context.Background(), "go")
	if err != nil || res.Answer != "ok" {
		t.Fatalf("got %+v, %v", res, err)
	}
	if !strings.Contains((*bodies)[1], "unknown tool nope") {
		t.Fatalf("error not sent back: %s", (*bodies)[1])
	}
}

func TestRunLastStepCannotCallTools(t *testing.T) {
	client, bodies := fakeAPI(t,
		`{"model":"m","content":[{"type":"tool_use","id":"t1","name":"x","input":{}}],"usage":{}}`,
		`{"model":"m","content":[{"type":"text","text":"final"}],"usage":{}}`,
	)
	a := &Agent{Client: client, MaxSteps: 2, Tools: []Tool{{Name: "x", Schema: objectSchema("a", "a"),
		Run: func(context.Context, json.RawMessage) (string, error) { return "r", nil }}}}
	res, err := a.Run(context.Background(), "go")
	if err != nil || res.Answer != "final" || res.Steps != 2 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if strings.Contains((*bodies)[0], "tool_choice") || !strings.Contains((*bodies)[1], `"tool_choice":{"type":"none"}`) {
		t.Fatalf("tool_choice wrong: %v", *bodies)
	}
}

func TestPageText(t *testing.T) {
	got := pageText("<html><style>p{}</style><body><p>Hello</p>  <script>x()</script><b>world</b></body></html>")
	if got != "Hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestFetchURLTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<p>page</p>"))
	}))
	defer srv.Close()
	tool := fetchURLTool(srv.Client(), agent_defs.Fetch{})
	out, err := tool.Run(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err != nil || out != "page" {
		t.Fatalf("got %q, %v", out, err)
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"url":"file:///etc/passwd"}`)); err == nil {
		t.Fatal("expected error for non-http url")
	}
}

func TestObserverSeesTheStepsAndToolsOfARun(t *testing.T) {
	client, _ := fakeAPI(t,
		`{"model":"m","content":[{"type":"tool_use","id":"t1","name":"echo","input":{"query":"hi"}}],"usage":{"input_tokens":1,"output_tokens":2}}`,
		`{"model":"m","content":[{"type":"tool_use","id":"t2","name":"echo","input":{}}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"model":"m","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":3,"output_tokens":4}}`,
	)
	echo := Tool{Name: "echo", Schema: objectSchema("query", "q"), Run: func(ctx context.Context, input json.RawMessage) (string, error) {
		// What the tool spends itself (a model call of its own) is added to the run.
		ReportUsage(ctx, 100, 10, 0.5)
		q, err := stringArg(input, "query")
		return "echo:" + q, err
	}}
	var events []string
	var lastTotal Result
	a := &Agent{Client: client, Tools: []Tool{echo}, MaxSteps: 3, Observer: Observer{
		BeforeModel: func(step int, final bool) { events = append(events, fmt.Sprintf("model %d final=%v", step, final)) },
		AfterModel: func(step int, total Result) {
			events = append(events, fmt.Sprintf("modeled %d steps=%d", step, total.Steps))
		},
		BeforeTool: func(step int, name string, input json.RawMessage) {
			events = append(events, fmt.Sprintf("tool %d %s %s", step, name, input))
		},
		AfterTool: func(step int, name, output string, isErr bool, total Result) {
			events = append(events, fmt.Sprintf("tooled %d %s %q err=%v", step, name, output, isErr))
			lastTotal = total
		},
	}}
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"model 1 final=false", "modeled 1 steps=1", `tool 1 echo {"query":"hi"}`, `tooled 1 echo "echo:hi" err=false`,
		"model 2 final=false", "modeled 2 steps=2", "tool 2 echo {}", `tooled 2 echo "missing \"query\"" err=true`,
		"model 3 final=true", "modeled 3 steps=3",
	}
	if strings.Join(events, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s", strings.Join(events, "\n"))
	}
	// 1+1+3 tokens in, and 100 twice for the tools; the cost is only that of the tools here.
	if res.InputTokens != 205 || res.OutputTokens != 27 || res.CostUSD != 1.0 || res.ToolCalls != 2 {
		t.Fatalf("result: %+v", res)
	}
	if lastTotal.InputTokens != 202 || lastTotal.CostUSD != 1.0 {
		t.Fatalf("total seen by the observer: %+v", lastTotal)
	}
}

func TestReportUsageOutsideARunDoesNothing(t *testing.T) {
	ReportUsage(context.Background(), 1, 1, 1) // no panic
}

// Runs of one Agent value at the same time share nothing: each keeps its own conversation and totals.
func TestConcurrentRunsOfOneAgentAreIsolated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		// The first call of a run (one message) asks for the tool; the second (it carries the tool result) answers with it.
		if !strings.Contains(string(b), "tool_result") {
			name := "a"
			if strings.Contains(string(b), `"content":"prompt-b"`) {
				name = "b"
			}
			fmt.Fprintf(w, `{"model":"m","content":[{"type":"tool_use","id":"t","name":"echo","input":{"query":%q}}],"usage":{"input_tokens":1,"output_tokens":1}}`, name)
			return
		}
		i := strings.Index(string(b), "echo:")
		fmt.Fprintf(w, `{"model":"m","content":[{"type":"text","text":%q}],"usage":{"input_tokens":1,"output_tokens":1}}`, string(b)[i:i+6])
	}))
	t.Cleanup(srv.Close)
	client, err := ai.New("k", ai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	echo := Tool{Name: "echo", Description: "echo", Schema: objectSchema("query", "q"),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			q, err := stringArg(input, "query")
			ReportUsage(ctx, 100, 100, 1)
			return "echo:" + q, err
		}}
	a := &Agent{Client: client, Tools: []Tool{echo}}

	type out struct {
		res *Result
		err error
	}
	results := make([]out, 20)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := "prompt-a"
			if i%2 == 1 {
				p = "prompt-b"
			}
			results[i].res, results[i].err = a.Run(context.Background(), p)
		}()
	}
	wg.Wait()
	for i, o := range results {
		want := "echo:a"
		if i%2 == 1 {
			want = "echo:b"
		}
		if o.err != nil || o.res.Answer != want || o.res.Steps != 2 || o.res.ToolCalls != 1 ||
			o.res.InputTokens != 102 || o.res.OutputTokens != 102 || o.res.CostUSD != 1 {
			t.Fatalf("run %d is not isolated: %+v, %v", i, o.res, o.err)
		}
	}
}

func TestTrajectoryIsOffByDefault(t *testing.T) {
	client, _ := fakeAPI(t, `{"model":"m","content":[{"type":"text","text":"done"}],"usage":{}}`)
	res, err := (&Agent{Client: client}).Run(context.Background(), "go")
	if err != nil || res.Trajectory != nil || res.TrajectoryPath != "" {
		t.Fatalf("got %+v, %v", res, err)
	}
}

func TestRunReturnsAndStoresTheTrajectory(t *testing.T) {
	client, _ := fakeAPI(t,
		`{"model":"m","stop_reason":"tool_use","content":[{"type":"text","text":"let me check"},{"type":"tool_use","id":"t1","name":"echo","input":{"query":"hi"}},{"type":"tool_use","id":"t2","name":"echo","input":{}}],"usage":{"input_tokens":1,"output_tokens":2}}`,
		`{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":3,"output_tokens":4}}`,
	)
	echo := Tool{Name: "echo", Schema: objectSchema("query", "q"), Run: func(ctx context.Context, input json.RawMessage) (string, error) {
		ReportUsage(ctx, 100, 10, 0.5)
		q, err := stringArg(input, "query")
		return "echo:" + q, err
	}}
	dir := filepath.Join(t.TempDir(), "atif")
	a := &Agent{Client: client, System: "sys", Tools: []Tool{echo}, Name: "my-agent", SessionID: "sess-1", Trajectory: true, TrajectoryDir: dir}
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Trajectory
	if tr == nil {
		t.Fatal("no trajectory")
	}
	if tr.Agent.Name != "my-agent" || tr.Agent.ModelName != "m" || tr.SessionID != "sess-1" || tr.SchemaVersion != atif.SchemaVersion {
		t.Fatalf("header: %+v", tr)
	}
	// system, user, the step with two tool calls, the final answer
	if len(tr.Steps) != 4 {
		t.Fatalf("steps: %+v", tr.Steps)
	}
	if tr.Steps[0].Source != atif.SourceSystem || tr.Steps[0].Message != "sys" || tr.Steps[1].Source != atif.SourceUser || tr.Steps[1].Message != "go" {
		t.Fatalf("prompts: %+v", tr.Steps[:2])
	}
	s := tr.Steps[2]
	if s.Message != "let me check" || s.Source != atif.SourceAgent || len(s.ToolCalls) != 2 || s.ToolCalls[0].ToolCallID != "t1" ||
		s.ToolCalls[0].FunctionName != "echo" || s.ToolCalls[0].Arguments["query"] != "hi" {
		t.Fatalf("tool step: %+v", s)
	}
	if got := s.Observation.Results; len(got) != 2 || got[0].SourceCallID != "t1" || got[0].Content != "echo:hi" || got[1].SourceCallID != "t2" {
		t.Fatalf("observation: %+v", got)
	}
	if s.Metrics.PromptTokens != 1 || s.Metrics.CompletionTokens != 2 {
		t.Fatalf("metrics: %+v", s.Metrics)
	}
	if failed, _ := s.Extra["failed_tool_calls"].([]string); len(failed) != 1 || failed[0] != "t2" {
		t.Fatalf("extra: %+v", s.Extra)
	}
	if u, _ := s.Extra["tools_usage"].(map[string]any); u["input_tokens"] != 200 || u["cost_usd"] != 1.0 {
		t.Fatalf("tools usage: %+v", s.Extra)
	}
	if tr.Steps[3].Message != "done" || tr.Steps[3].ToolCalls != nil {
		t.Fatalf("last step: %+v", tr.Steps[3])
	}
	if tr.FinalMetrics.TotalPromptTokens != 4 || tr.FinalMetrics.TotalCompletionTokens != 6 {
		t.Fatalf("final metrics: %+v", tr.FinalMetrics)
	}
	if r, _ := tr.Extra["result"].(map[string]any); r["input_tokens"] != res.InputTokens || r["answer"] != "done" {
		t.Fatalf("result extra: %+v", tr.Extra)
	}
	if want := filepath.Join(dir, tr.TrajectoryID+".json"); res.TrajectoryPath != want {
		t.Fatalf("path %q, want %q", res.TrajectoryPath, want)
	}
	if _, err := os.Stat(res.TrajectoryPath); err != nil {
		t.Fatal(err)
	}
}

func TestFailedRunStillHasATrajectory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"nope"}}`))
	}))
	t.Cleanup(srv.Close)
	client, err := ai.New("k", ai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&Agent{Client: client, Trajectory: true}).Run(context.Background(), "go")
	if err == nil {
		t.Fatal("expected an error")
	}
	if res.Trajectory == nil || !strings.HasPrefix(res.Trajectory.Notes, "failed: ") || res.TrajectoryPath != "" {
		t.Fatalf("trajectory: %+v", res)
	}
}

func fetchOf(t *testing.T, tool Tool, url string) (string, error) {
	t.Helper()
	return tool.Run(context.Background(), json.RawMessage(`{"url":"`+url+`"}`))
}

func TestFetchURLToolDomains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://other.example.org/x", http.StatusFound)
			return
		}
		w.Write([]byte("<p>page</p>"))
	}))
	defer srv.Close()

	// The test server is 127.0.0.1: nothing of the lists matches it but a list that has it.
	blocked := fetchURLTool(srv.Client(), agent_defs.Fetch{BlockedDomains: []string{"127.0.0.1"}})
	if _, err := fetchOf(t, blocked, srv.URL); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("blocked: %v", err)
	}
	allowedOther := fetchURLTool(srv.Client(), agent_defs.Fetch{AllowedDomains: []string{"example.com"}})
	if _, err := fetchOf(t, allowedOther, srv.URL); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("allowed list without the host: %v", err)
	}
	allowed := fetchURLTool(srv.Client(), agent_defs.Fetch{AllowedDomains: []string{"127.0.0.1"}})
	if out, err := fetchOf(t, allowed, srv.URL); err != nil || out != "page" {
		t.Fatalf("allowed host: %q, %v", out, err)
	}
	// A redirect may not leave the allowed sites.
	if _, err := fetchOf(t, allowed, srv.URL+"/redirect"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("redirect out of the allowed sites: %v", err)
	}
}

func TestHostMatches(t *testing.T) {
	list := []string{"example.com"}
	for host, want := range map[string]bool{"example.com": true, "www.example.com": true, "EXAMPLE.com.": true, "badexample.com": false, "example.com.evil.org": false} {
		if got := hostMatches(host, list); got != want {
			t.Errorf("%s: %v", host, got)
		}
	}
}

func TestFetchURLToolCallLimitAndPageSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<p>" + strings.Repeat("x", 5000) + "</p>"))
	}))
	defer srv.Close()
	tool := fetchURLTool(srv.Client(), agent_defs.Fetch{MaxCalls: 2, MaxPageChars: 1000})
	for i := 0; i < 2; i++ {
		out, err := fetchOf(t, tool, srv.URL)
		if err != nil || len(out) != 1000 {
			t.Fatalf("call %d: %d chars, %v", i, len(out), err)
		}
	}
	if _, err := fetchOf(t, tool, srv.URL); err == nil || !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("third call: %v", err)
	}
}

func TestWebSearchToolSendsTheDomainsAndLocationAndCountsCalls(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Write([]byte(`{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"found"}],"usage":{}}`))
	}))
	defer srv.Close()
	client, err := ai.New("k", ai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	tool := webSearchTool(client, agent_defs.Search{
		AllowedDomains: []string{"reuters.com"}, PreferredDomains: []string{"apnews.com"}, MaxUsesPerCall: 4, MaxCalls: 1,
		Location: &agent_defs.Location{Country: "DE", City: "Munich"}, Instructions: "Primary sources first.",
	})
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"query":"q"}`)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"allowed_domains":["reuters.com"]`, `"max_uses":4`, `"user_location":{`, `"country":"DE"`, `"city":"Munich"`, `Primary sources first.`, `apnews.com`} {
		if !strings.Contains(bodies[0], want) {
			t.Errorf("the request lacks %s: %s", want, bodies[0])
		}
	}
	if strings.Contains(bodies[0], "blocked_domains") {
		t.Errorf("empty blocked_domains sent: %s", bodies[0])
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"query":"q"}`)); err == nil || !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("second call: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("the refused call reached the API: %d", len(bodies))
	}
}

func TestOutOfBudgetMakesTheNextCallTheLastOne(t *testing.T) {
	client, bodies := fakeAPI(t,
		`{"model":"m","content":[{"type":"tool_use","id":"t1","name":"x","input":{}}],"usage":{}}`,
		`{"model":"m","content":[{"type":"text","text":"report from what I have"}],"usage":{}}`,
	)
	calls := 0
	a := &Agent{Client: client, MaxSteps: 10, Tools: []Tool{{Name: "x", Schema: objectSchema("a", "a"),
		Run: func(context.Context, json.RawMessage) (string, error) { return "r", nil }}},
		OutOfBudget: func() string {
			calls++
			if calls >= 2 {
				return "The cost cap was reached."
			}
			return ""
		}}
	var finals []bool
	a.Observer.BeforeModel = func(step int, final bool) { finals = append(finals, final) }
	res, err := a.Run(context.Background(), "go")
	if err != nil || res.Answer != "report from what I have" || res.Steps != 2 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if res.OutOfBudget != "The cost cap was reached." {
		t.Errorf("OutOfBudget: %q", res.OutOfBudget)
	}
	if fmt.Sprint(finals) != "[false true]" {
		t.Errorf("final flags: %v", finals)
	}
	second := (*bodies)[1]
	if !strings.Contains(second, `"tool_choice":{"type":"none"}`) || !strings.Contains(second, "[Budget reached] The cost cap was reached.") {
		t.Fatalf("the last call is not told: %s", second)
	}
	if strings.Contains((*bodies)[0], "Budget reached") {
		t.Errorf("the first call was told of a budget: %s", (*bodies)[0])
	}
}

func TestOutOfBudgetNeverCutsAnAgentThatIsWithinIt(t *testing.T) {
	client, _ := fakeAPI(t, `{"model":"m","content":[{"type":"text","text":"done"}],"usage":{}}`)
	a := &Agent{Client: client, OutOfBudget: func() string { return "" }}
	res, err := a.Run(context.Background(), "go")
	if err != nil || res.OutOfBudget != "" || res.Answer != "done" {
		t.Fatalf("got %+v, %v", res, err)
	}
}

func TestFromDefBuildsTheAgentOfADefinition(t *testing.T) {
	def := agent_defs.Builtin()
	def.Name, def.Version = "scout", "2.1.0"
	def.SystemPrompt = "You are a scout."
	def.Model, def.MaxSteps, def.MaxTokens = "my-model", 7, 1234
	def.Tools = []string{agent_defs.ToolWebSearch}
	def.Search.PreferredDomains = []string{"apnews.com"}
	def.Search.Recency = agent_defs.RecencyWeek
	def.Search.Fetch.AllowedDomains = []string{"example.com"}
	def.Knowledge.Access = agent_defs.AccessReadOnly
	def.Knowledge.QueryFirst = true
	def.Knowledge.Instructions = "Be terse about the knowledge base."
	a := FromDef(def, nil, nil, &fakeKB{})
	if a.Name != "gligabao-scout" || a.Version != "2.1.0" || a.Model != "my-model" || a.MaxSteps != 7 || a.MaxTokens != 1234 {
		t.Errorf("settings: %+v", a)
	}
	if got := toolNames(a); got != "web_search,query" {
		t.Errorf("tools: %s", got)
	}
	for _, want := range []string{"You are a scout.", "apnews.com", "the last week", "which you can only read", "Always start by asking the knowledge base", "Be terse about the knowledge base."} {
		if !strings.Contains(a.System, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, a.System)
		}
	}
	if strings.Contains(a.System, "remember") {
		t.Errorf("a read-only agent is told about remember:\n%s", a.System)
	}
	def.Knowledge.Access = agent_defs.AccessNone
	if got := toolNames(FromDef(def, nil, nil, &fakeKB{})); got != "web_search" {
		t.Errorf("no knowledge access: %s", got)
	}
	AddSkills(a, []agent_defs.Skill{{Name: "dates", Content: "Always give dates."}})
	if !strings.HasSuffix(a.System, "## Skill: dates\n\nAlways give dates.") {
		t.Errorf("the skills are not last:\n%s", a.System)
	}
	if n := FromDef(agent_defs.Builtin(), nil, nil, nil).Name; n != "gligabao-research" {
		t.Errorf("the name of the default agent in a trajectory: %s", n)
	}
}

func toolNames(a *Agent) string {
	var names []string
	for _, t := range a.Tools {
		names = append(names, t.Name)
	}
	return strings.Join(names, ",")
}

// A terminal tool ends the run when it succeeds; when it fails the model is told and may call it again; a text answer is told to call
// it; and the last step is forced to call it instead of being tool-free.
func TestTerminalToolEndsTheRun(t *testing.T) {
	client, bodies := fakeAPI(t,
		`{"model":"m","content":[{"type":"tool_use","id":"t1","name":"submit","input":{"v":"bad"}}],"usage":{}}`,
		`{"model":"m","content":[{"type":"text","text":"I am done"}],"usage":{}}`,
		`{"model":"m","content":[{"type":"tool_use","id":"t2","name":"submit","input":{"v":"good"}}],"usage":{}}`,
	)
	submit := SubmitFindingsTool(objectSchema("v", "v"), func(raw json.RawMessage) error {
		if v, _ := stringArg(raw, "v"); v != "good" {
			return fmt.Errorf("v is %s", v)
		}
		return nil
	})
	submit.Name = "submit"
	res, err := (&Agent{Client: client, Terminal: "submit", Tools: []Tool{submit}, MaxSteps: 5}).Run(context.Background(), "go")
	if err != nil || res.Steps != 3 || string(res.Final) != `{"v":"good"}` {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains((*bodies)[1], "v is bad") || !strings.Contains((*bodies)[2], "Call submit now") {
		t.Errorf("the model was not told: %v", *bodies)
	}
}

func TestTerminalToolIsForcedOnTheLastStep(t *testing.T) {
	client, bodies := fakeAPI(t,
		`{"model":"m","content":[{"type":"tool_use","id":"t1","name":"x","input":{}}],"usage":{}}`,
		`{"model":"m","content":[{"type":"tool_use","id":"t2","name":"submit","input":{"v":"good"}}],"usage":{}}`,
	)
	tools := []Tool{
		{Name: "x", Schema: objectSchema("a", "a"), Run: func(context.Context, json.RawMessage) (string, error) { return "r", nil }},
		{Name: "submit", Schema: objectSchema("v", "v"), Run: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }},
	}
	res, err := (&Agent{Client: client, Terminal: "submit", Tools: tools, MaxSteps: 2}).Run(context.Background(), "go")
	if err != nil || res.Final == nil {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains((*bodies)[1], `"tool_choice":{"name":"submit","type":"tool"}`) {
		t.Errorf("the last step is not forced: %s", (*bodies)[1])
	}
}
