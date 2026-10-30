// Package mcp serves gligabao's remember and query over the Model Context Protocol (MCP), as two tools on one endpoint
// (POST /v1/gb/mcp, streamable HTTP transport), built on the official Go SDK (github.com/modelcontextprotocol/go-sdk).
//
// The tools do not reimplement anything: a tool call is turned into the JSON body (or the query string) of the matching REST endpoint
// and run through the very same handler (so validation, limits, monitoring events and results are those of POST /v1/gb/remember and
// POST /v1/gb/query), in process, without a network hop. The research agent tools (research_start, research_list, research_get) are
// there only when the handlers of the research endpoints are given (WithResearch). There is no authentication yet.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Path is the route of the MCP endpoint.
const Path = "/v1/gb/mcp"

// Tool names.
const (
	ToolRemember = "remember"
	ToolQuery    = "query"

	ToolResearchStart = "research_start"
	ToolResearchList  = "research_list"
	ToolResearchGet   = "research_get"
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
	UserID      string            `json:"user_id" jsonschema:"the user that owns the dataset"`
	Query       string            `json:"query" jsonschema:"the question to answer from the knowledge graph"`
	Dataset     string            `json:"dataset,omitempty" jsonschema:"the dataset to search (default main)"`
	SessionID   string            `json:"session_id,omitempty" jsonschema:"a label for the user/agent session the call belongs to"`
	Reasoning   string            `json:"reasoning,omitempty" jsonschema:"reasoning level of the answering LLM call: none (default), low, medium or high"`
	Models      map[string]string `json:"models,omitempty" jsonschema:"the model of each stage of the query: query (the answer), detect_geo (the places in the question, when geo is on); default: the server's"`
	GeoNearby   *bool             `json:"geo_nearby,omitempty" jsonschema:"set true to give the geo entities near the places of the question as context (off by default; needs geo on in the server); places of the question that are found are stored"`
	GeoRadiusKm *float64          `json:"geo_radius_km,omitempty" jsonschema:"radius in km of that nearby search (default: the server's, 50); setting it turns the search on"`
}

// ResearchStartInput is the input of the research_start tool: the body of POST /v1/gb/research.
type ResearchStartInput struct {
	UserID   string `json:"user_id" jsonschema:"the user that owns the research"`
	Topic    string `json:"topic" jsonschema:"what to research: a question, or a subject to write a report on (at most 2000 characters)"`
	MaxSteps int    `json:"max_steps,omitempty" jsonschema:"the most model calls the agent makes, the last of which writes the report (default 20, at most 40); more steps research more and cost more"`
	Model    string `json:"model,omitempty" jsonschema:"the model the agent runs on, one that supports Anthropic's web search tool (default: the server's)"`
	Dataset  string `json:"dataset,omitempty" jsonschema:"the dataset of the user's knowledge base the agent queries and remembers into (default main)"`
}

// ResearchListInput is the input of the research_list tool: the query of GET /v1/gb/research.
type ResearchListInput struct {
	UserID string `json:"user_id" jsonschema:"the user that owns the researches"`
	Active *bool  `json:"active,omitempty" jsonschema:"set true to list only the agents that are working now"`
	Status string `json:"status,omitempty" jsonschema:"only the researches with this status: running, completed or failed (not together with active)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"the most researches returned, newest first (default 50, at most 500)"`
}

// ResearchGetInput is the input of the research_get tool: the parameters of GET /v1/gb/research/{id}.
type ResearchGetInput struct {
	UserID string `json:"user_id" jsonschema:"the user that owns the research"`
	ID     int64  `json:"id" jsonschema:"the id research_start returned"`
}

// Option configures New and Register.
type Option func(*options)

type options struct {
	researchStart, researchList, researchGet http.Handler
}

// WithResearch adds the tools of the research agents: research_start, research_list and research_get run the handlers of
// POST /v1/gb/research, GET /v1/gb/research and GET /v1/gb/research/{id} (without any authentication wrapper).
func WithResearch(start, list, get http.Handler) Option {
	return func(o *options) { o.researchStart, o.researchList, o.researchGet = start, list, get }
}

// New returns the handler of the MCP endpoint. rememberH and queryH are the handlers of POST /v1/gb/remember and
// POST /v1/gb/query (without any authentication wrapper); version is reported to clients as the server's version.
func New(rememberH, queryH http.Handler, version string, opts ...Option) http.Handler {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
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
	if o.researchStart != nil && o.researchList != nil && o.researchGet != nil {
		sdk.AddTool(s, &sdk.Tool{
			Name: ToolResearchStart,
			Description: "Start a deep-research agent on a topic. It searches the web and reads pages over many steps, in the background, and ends with a report. " +
				"Returns the run right away (an id and status running); follow it with research_get, which gives its state and, when it is completed, the report. The run costs money (model calls and web searches).",
			Annotations: &sdk.ToolAnnotations{Title: "Start a research agent", DestructiveHint: boolPtr(false)},
		}, forward[ResearchStartInput](o.researchStart))
		sdk.AddTool(s, &sdk.Tool{
			Name:        ToolResearchList,
			Description: "List the research agents of a user, newest first: their status, current state, steps and cost. With active set, only the ones that are working now.",
			Annotations: &sdk.ToolAnnotations{Title: "List research agents", ReadOnlyHint: true, OpenWorldHint: boolPtr(false)},
		}, forwardGet(o.researchList, func(in ResearchListInput) (url.Values, map[string]string) {
			q := url.Values{"user_id": {in.UserID}}
			if in.Active != nil {
				q.Set("active", strconv.FormatBool(*in.Active))
			}
			if in.Status != "" {
				q.Set("status", in.Status)
			}
			if in.Limit != 0 {
				q.Set("limit", strconv.Itoa(in.Limit))
			}
			return q, nil
		}))
		sdk.AddTool(s, &sdk.Tool{
			Name:        ToolResearchGet,
			Description: "Read a research agent: its status, current state, cost and, in order, every state it went through (searching, reading, thinking, ...) with what each was about (the query, the URL). The report is in it once the status is completed.",
			Annotations: &sdk.ToolAnnotations{Title: "Get a research agent", ReadOnlyHint: true, OpenWorldHint: boolPtr(false)},
		}, forwardGet(o.researchGet, func(in ResearchGetInput) (url.Values, map[string]string) {
			return url.Values{"user_id": {in.UserID}}, map[string]string{"id": strconv.FormatInt(in.ID, 10)}
		}))
	}

	// Stateless: the tools are plain request/response calls, so no session is kept between them.
	return sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, &sdk.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
}

// Register registers the MCP endpoint on mux (one route per method, so that it does not conflict with a "GET /" of the mux).
func Register(mux *http.ServeMux, rememberH, queryH http.Handler, version string, opts ...Option) {
	h := New(rememberH, queryH, version, opts...)
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
		return serve(h, req), nil, nil
	}
}

// forwardGet makes the tool handler that sends a GET to h, with the query string and the path values (the {name}s of the route) that
// params makes of its input, and returns h's answer like forward.
func forwardGet[In any](h http.Handler, params func(In) (query url.Values, path map[string]string)) sdk.ToolHandlerFor[In, any] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, any, error) {
		query, path := params(in)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/?"+query.Encode(), nil)
		if err != nil {
			return nil, nil, err
		}
		for k, v := range path {
			req.SetPathValue(k, v)
		}
		return serve(h, req), nil, nil
	}
}

// serve runs h on req in process and turns its response into the result of a tool.
func serve(h http.Handler, req *http.Request) *sdk.CallToolResult {
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	h.ServeHTTP(rec, req)
	if rec.status >= 400 {
		return toolError(rec.status, rec.body.Bytes())
	}
	res := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: strings.TrimSpace(rec.body.String())}}}
	var structured map[string]any
	if json.Unmarshal(rec.body.Bytes(), &structured) == nil && structured != nil {
		res.StructuredContent = structured
	}
	return res
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
