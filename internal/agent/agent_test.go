package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
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
	tool := fetchURLTool(srv.Client())
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
