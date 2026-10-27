package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
)

// connect serves the MCP endpoint (with the given stub handlers) and returns a client session on it.
func connect(t *testing.T, rememberH, queryH http.HandlerFunc) *sdk.ClientSession {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(http.ResponseWriter, *http.Request) {}) // a host's catch-all must not conflict with the MCP routes
	Register(mux, rememberH, queryH, "1.2.3")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: srv.URL + Path, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func text(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("content: %+v", res.Content)
	}
	tc, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is %T", res.Content[0])
	}
	return tc.Text
}

func TestListTools(t *testing.T) {
	cs := connect(t, nil, nil)
	if got := cs.InitializeResult().ServerInfo; got.Name != "gligabao" || got.Version != "1.2.3" {
		t.Fatalf("server info: %+v", got)
	}
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "query,remember" && strings.Join(names, ",") != "remember,query" || len(names) != 2 {
		t.Fatalf("tools: %v", names)
	}
}

func TestRememberForwardsToTheHandler(t *testing.T) {
	var gotPath, gotMethod string
	var got map[string]any
	cs := connect(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"artifact_id": 7, "kind": "knowledge"})
	}, nil)
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: ToolRemember, Arguments: map[string]any{
		"user_id": "u1", "text": "Alice works at Acme.", "kind": "episode", "session_id": "s1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", text(t, res))
	}
	if gotMethod != "POST" || got["user_id"] != "u1" || got["text"] != "Alice works at Acme." || got["kind"] != "episode" || got["session_id"] != "s1" {
		t.Fatalf("handler got %s %s %v", gotMethod, gotPath, got)
	}
	if _, ok := got["dataset"]; ok {
		t.Fatalf("unset optional field was sent: %v", got)
	}
	sc, _ := res.StructuredContent.(map[string]any)
	if sc["kind"] != "knowledge" || sc["artifact_id"] != float64(7) {
		t.Fatalf("structured content: %#v", res.StructuredContent)
	}
	if !strings.Contains(text(t, res), `"artifact_id":7`) {
		t.Fatalf("text: %s", text(t, res))
	}
}

func TestQueryForwardsToTheHandler(t *testing.T) {
	var got map[string]any
	cs := connect(t, nil, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"answer": "Acme"})
	})
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: ToolQuery, Arguments: map[string]any{
		"user_id": "u1", "query": "Where does Alice work?", "dataset": "work", "reasoning": "low",
	}})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	if got["query"] != "Where does Alice work?" || got["dataset"] != "work" || got["reasoning"] != "low" {
		t.Fatalf("handler got %v", got)
	}
	if sc, _ := res.StructuredContent.(map[string]any); sc["answer"] != "Acme" {
		t.Fatalf("structured content: %#v", res.StructuredContent)
	}
}

func TestHandlerErrorIsAToolError(t *testing.T) {
	cs := connect(t, nil, func(w http.ResponseWriter, r *http.Request) {
		httputil.Error(w, http.StatusNotFound, "dataset not found")
	})
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: ToolQuery, Arguments: map[string]any{"user_id": "u1", "query": "q"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(text(t, res), "dataset not found") || !strings.Contains(text(t, res), "404") {
		t.Fatalf("result: %+v", res)
	}
}

func TestMissingRequiredArgumentIsRejected(t *testing.T) {
	called := false
	cs := connect(t, func(http.ResponseWriter, *http.Request) { called = true }, nil)
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: ToolRemember, Arguments: map[string]any{"user_id": "u1"}})
	if err == nil && (res == nil || !res.IsError) {
		t.Fatalf("expected a rejection, got %+v", res)
	}
	if called {
		t.Fatal("the handler ran for an invalid input")
	}
}
