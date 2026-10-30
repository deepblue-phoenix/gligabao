package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
