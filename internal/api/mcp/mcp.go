// Package mcp serves gligabao's remember and query over the Model Context Protocol (MCP), as two tools on one endpoint
// (POST /v1/gb/mcp, streamable HTTP transport), built on the official Go SDK (github.com/modelcontextprotocol/go-sdk).
//
// The tools do not reimplement anything: a tool call is turned into the JSON body of the matching REST endpoint and run through
// the very same handler (so validation, limits, monitoring events and results are those of POST /v1/gb/remember and
// POST /v1/gb/query), in process, without a network hop. There is no authentication yet.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Path is the route of the MCP endpoint.
const Path = "/v1/gb/mcp"

// Tool names.
const (
	ToolRemember = "remember"
	ToolQuery    = "query"
)

// RememberInput is the input of the remember tool: the text-only subset of the body of POST /v1/gb/remember.
type RememberInput struct {
	UserID        string `json:"user_id" jsonschema:"the user that owns the dataset"`
	Text          string `json:"text" jsonschema:"the text to remember (at most 1 MiB)"`
	Dataset       string `json:"dataset,omitempty" jsonschema:"the dataset to store into, created if missing (default main)"`
	Kind          string `json:"kind,omitempty" jsonschema:"knowledge (default) or episode"`
	SessionID     string `json:"session_id,omitempty" jsonschema:"a label for the user/agent session the call belongs to"`
	ReferenceTime string `json:"reference_time,omitempty" jsonschema:"when the text was written, RFC 3339 (default now); relative dates in the text are resolved against it"`
}

// QueryInput is the input of the query tool: the body of POST /v1/gb/query.
type QueryInput struct {
	UserID    string            `json:"user_id" jsonschema:"the user that owns the dataset"`
	Query     string            `json:"query" jsonschema:"the question to answer from the knowledge graph"`
	Dataset   string            `json:"dataset,omitempty" jsonschema:"the dataset to search (default main)"`
	SessionID string            `json:"session_id,omitempty" jsonschema:"a label for the user/agent session the call belongs to"`
	Reasoning string            `json:"reasoning,omitempty" jsonschema:"reasoning level of the answering LLM call: none (default), low, medium or high"`
	Models    map[string]string `json:"models,omitempty" jsonschema:"the model of each stage of the query: query (the answer), detect_geo (the places in the question, when geo is on); default: the server's"`
}

// New returns the handler of the MCP endpoint. rememberH and queryH are the handlers of POST /v1/gb/remember and
// POST /v1/gb/query (without any authentication wrapper); version is reported to clients as the server's version.
func New(rememberH, queryH http.Handler, version string) http.Handler {
	s := sdk.NewServer(&sdk.Implementation{Name: "gligabao", Title: "gligabao memory", Version: version}, &sdk.ServerOptions{
		Instructions: "gligabao is a temporal knowledge graph memory. Use remember to store a text in it (facts are extracted and stored with the time they became true) and query to answer a question from what was remembered.",
	})
	sdk.AddTool(s, &sdk.Tool{
		Name:        ToolRemember,
		Description: "Store a text in memory: entities and facts are extracted from it into a knowledge graph. Returns the artifact, the entities and the facts (edges) it yielded.",
		Annotations: &sdk.ToolAnnotations{Title: "Remember", DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)},
	}, forward[RememberInput](rememberH))
	sdk.AddTool(s, &sdk.Tool{
		Name:        ToolQuery,
		Description: "Answer a question from what was remembered. Returns the answer and the evidence (entities and facts) it was based on.",
		Annotations: &sdk.ToolAnnotations{Title: "Query", ReadOnlyHint: true, OpenWorldHint: boolPtr(false)},
	}, forward[QueryInput](queryH))

	// Stateless: the tools are plain request/response calls, so no session is kept between them.
	return sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, &sdk.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
}

// Register registers the MCP endpoint on mux (one route per method, so that it does not conflict with a "GET /" of the mux).
func Register(mux *http.ServeMux, rememberH, queryH http.Handler, version string) {
	h := New(rememberH, queryH, version)
	for _, m := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		mux.Handle(m+" "+Path, h)
	}
}

func boolPtr(b bool) *bool { return &b }

// forward makes the tool handler that sends its input as the JSON body of a POST to h and returns h's answer: the JSON response as
// the structured content (and as text), or, for a status of 400 and over, a tool error with the message of the response.
func forward[In any](h http.Handler) sdk.ToolHandlerFor[In, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, any, error) {
		body, err := json.Marshal(in)
		if err != nil {
			return nil, nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("content-type", "application/json")
		rec := &recorder{header: http.Header{}, status: http.StatusOK}
		h.ServeHTTP(rec, req)

		if rec.status >= 400 {
			return toolError(rec.status, rec.body.Bytes()), nil, nil
		}
		res := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: strings.TrimSpace(rec.body.String())}}}
		var structured map[string]any
		if json.Unmarshal(rec.body.Bytes(), &structured) == nil && structured != nil {
			res.StructuredContent = structured
		}
		return res, nil, nil
	}
}

// toolError is the result of a call the handler rejected or failed: the handler's {"error": "..."} message.
func toolError(status int, body []byte) *sdk.CallToolResult {
	var e struct {
		Error string `json:"error"`
	}
	msg := ""
	if json.Unmarshal(body, &e) == nil {
		msg = e.Error
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("%s (status %d)", msg, status)}}}
}

// recorder is an http.ResponseWriter that keeps the response in memory.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(status int)      { r.status = status }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
