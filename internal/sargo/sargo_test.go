package sargo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

const schemaPath = "../../rust/sargo/inference.schema.json"

// schemaFor returns a validator for one definition ("InferenceRequest", "InferenceResponse", ...) of the sargo JSON Schema.
func schemaFor(t *testing.T, def string) *jsonschema.Resolved {
	t.Helper()
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	doc["$ref"] = "#/$defs/" + def
	data, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("load schema: %v", err)
	}
	r, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve schema: %v", err)
	}
	return r
}

// mustValidate checks that the JSON of v complies with the schema definition def.
func mustValidate(t *testing.T, def string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var inst any
	if err := json.Unmarshal(data, &inst); err != nil {
		t.Fatal(err)
	}
	if err := schemaFor(t, def).Validate(inst); err != nil {
		t.Fatalf("%s does not comply with the schema: %v\n%s", def, err, data)
	}
}

func fullRequest() *Request {
	temp := 0.0
	return &Request{
		Provider: ProviderAnthropic,
		Model:    "claude-sonnet-4-5",
		System:   "You are helpful.",
		Messages: []Message{
			UserText("Weather in Oslo?"),
			{Role: RoleAssistant, Content: []ContentBlock{
				TextBlock("Let me check."),
				{Type: BlockToolUse, ID: "tu_1", Name: "weather", Input: json.RawMessage(`{"city":"Oslo"}`)},
			}},
			{Role: RoleUser, Content: []ContentBlock{
				ToolResultBlock("tu_1", "3C, rain", false),
				ToolResultBlock("tu_2", "boom", true),
			}},
		},
		Tools: []Tool{{
			Name:        "weather",
			Description: "Get weather",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []string{"city"},
			},
		}},
		MaxTokens:   256,
		Temperature: &temp,
	}
}

func TestRequestComplyWithSchema(t *testing.T) {
	mustValidate(t, "InferenceRequest", fullRequest())

	minimal := &Request{Provider: ProviderOpenAI, Model: "gpt-4o", Messages: []Message{UserText("hi")}}
	mustValidate(t, "InferenceRequest", minimal)

	// A tool_use block without input and a message without content still produce valid JSON.
	odd := &Request{Provider: ProviderOpenAI, Model: "gpt-4o", Messages: []Message{
		{Role: RoleAssistant, Content: []ContentBlock{{Type: BlockToolUse, ID: "a", Name: "b"}}},
		{Role: RoleUser},
	}}
	mustValidate(t, "InferenceRequest", odd)
}

func TestSchemaRejectsWhatValidateRejects(t *testing.T) {
	// Sanity check of the test helper itself: the schema must reject an unknown provider.
	var inst any
	if err := json.Unmarshal([]byte(`{"provider":"x","model":"m","messages":[{"role":"user","content":[]}]}`), &inst); err != nil {
		t.Fatal(err)
	}
	if err := schemaFor(t, "InferenceRequest").Validate(inst); err == nil {
		t.Fatal("schema accepted an unknown provider")
	}
}

func TestResponseDecodesAndComplyWithSchema(t *testing.T) {
	const reply = `{"provider":"anthropic","model":"claude-sonnet-4-5","content":[
		{"type":"text","text":"Checking."},
		{"type":"tool_use","id":"tu_1","name":"weather","input":{"city":"Oslo"}}],
		"stop_reason":"tool_use","usage":{"input_tokens":50,"output_tokens":20}}`
	var inst any
	if err := json.Unmarshal([]byte(reply), &inst); err != nil {
		t.Fatal(err)
	}
	if err := schemaFor(t, "InferenceResponse").Validate(inst); err != nil {
		t.Fatalf("test reply does not comply with the schema: %v", err)
	}

	var resp Response
	if err := json.Unmarshal([]byte(reply), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Provider != ProviderAnthropic || resp.StopReason != StopToolUse || resp.Usage != (Usage{50, 20}) {
		t.Fatalf("bad response: %+v", resp)
	}
	if resp.Text() != "Checking." {
		t.Fatalf("Text() = %q", resp.Text())
	}
	uses := resp.ToolUses()
	if len(uses) != 1 || uses[0].ID != "tu_1" || uses[0].Name != "weather" || string(uses[0].Input) != `{"city":"Oslo"}` {
		t.Fatalf("bad tool uses: %+v", uses)
	}

	// The answer goes back as an assistant message, unchanged, and that is still valid.
	req := &Request{Provider: ProviderAnthropic, Model: "m", Messages: []Message{
		UserText("Weather in Oslo?"),
		resp.AssistantMessage(),
		{Role: RoleUser, Content: []ContentBlock{ToolResultBlock("tu_1", "rain", false)}},
	}}
	mustValidate(t, "InferenceRequest", req)
}

func TestContentBlockRoundTrip(t *testing.T) {
	blocks := []ContentBlock{
		TextBlock("hi"),
		{Type: BlockToolUse, ID: "i", Name: "n", Input: json.RawMessage(`{"a":1}`)},
		ToolResultBlock("i", "out", true),
	}
	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	var got []ContentBlock
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(blocks, got) {
		t.Fatalf("round trip changed the blocks:\n%+v\n%+v", blocks, got)
	}
	if err := json.Unmarshal([]byte(`[{"type":"image"}]`), &got); err == nil {
		t.Fatal("unknown block type accepted")
	}
	if _, err := json.Marshal(ContentBlock{Type: "image"}); err == nil {
		t.Fatal("unknown block type marshalled")
	}
}

func TestValidate(t *testing.T) {
	good := func() *Request { return fullRequest() }
	if err := good().Validate(); err != nil {
		t.Fatalf("good request rejected: %v", err)
	}
	cases := map[string]func(r *Request){
		"provider":    func(r *Request) { r.Provider = "google" },
		"model":       func(r *Request) { r.Model = "" },
		"no messages": func(r *Request) { r.Messages = nil },
		"role":        func(r *Request) { r.Messages[0].Role = "system" },
		"block type":  func(r *Request) { r.Messages[0].Content[0].Type = "image" },
		"input":       func(r *Request) { r.Messages[1].Content[1].Input = json.RawMessage(`[1]`) },
		"tool name":   func(r *Request) { r.Tools[0].Name = "bad name" },
		"long name":   func(r *Request) { r.Tools[0].Name = string(make([]byte, 65)) },
		"tool schema": func(r *Request) { r.Tools[0].InputSchema = map[string]any{"type": "string"} },
		"temperature": func(r *Request) { v := -1.0; r.Temperature = &v },
	}
	for name, mutate := range cases {
		r := good()
		mutate(r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: invalid request accepted", name)
		}
	}
	var nilReq *Request
	if err := nilReq.Validate(); err == nil {
		t.Error("nil request accepted")
	}
}

func TestInfer(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/infer" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.ContentLength <= 0 || len(r.TransferEncoding) != 0 {
			t.Errorf("body must have a Content-Length, got %d / %v", r.ContentLength, r.TransferEncoding)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"openai","model":"gpt-4o-2024","content":[{"type":"text","text":"Hello"}],` +
			`"stop_reason":"stop","usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Infer(context.Background(), &Request{Provider: ProviderOpenAI, Model: "gpt-4o", Messages: []Message{UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "Hello" || resp.Model != "gpt-4o-2024" || resp.StopReason != "stop" || resp.Usage.OutputTokens != 1 {
		t.Fatalf("bad response: %+v", resp)
	}
	var inst any
	if err := json.Unmarshal(gotBody, &inst); err != nil {
		t.Fatal(err)
	}
	if err := schemaFor(t, "InferenceRequest").Validate(inst); err != nil {
		t.Fatalf("the request on the wire does not comply with the schema: %v\n%s", err, gotBody)
	}
}

func TestInferErrors(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		msg       string
		retryable bool
	}{
		{400, `{"error":"bad json"}`, "bad json", false},
		{502, `{"error":"provider failed"}`, "provider failed", true},
		{503, `{"error":"no key"}`, "no key", false},
		{500, `not json`, "not json", false},
		{502, ``, "Bad Gateway", true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		c, err := New(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Infer(context.Background(), &Request{Provider: ProviderOpenAI, Model: "m", Messages: []Message{UserText("hi")}})
		srv.Close()
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("status %d: want *APIError, got %v", tc.status, err)
		}
		if apiErr.Status != tc.status || apiErr.Message != tc.msg || apiErr.Retryable() != tc.retryable {
			t.Errorf("status %d: got %+v (retryable %v)", tc.status, apiErr, apiErr.Retryable())
		}
	}
}

func TestInferRejectsInvalidRequestWithoutCalling(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	c, _ := New(srv.URL)
	if _, err := c.Infer(context.Background(), &Request{Provider: "nope"}); err == nil {
		t.Fatal("invalid request accepted")
	}
	if called {
		t.Fatal("server was called for an invalid request")
	}
}

func TestHealth(t *testing.T) {
	status := `{"status":"ok"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(status))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = `{"status":"down"}`
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("bad health status accepted")
	}
}

func TestNewEmptyURL(t *testing.T) {
	if _, err := New("  "); err == nil {
		t.Fatal("empty URL accepted")
	}
}
