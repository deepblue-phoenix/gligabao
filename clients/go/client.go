// Package gligabao is a small Go client for the gligabao HTTP API (a gligabao or gligabao_int server, e.g. run as a sidecar).
// It only uses the standard library. For now it wraps POST /v1/gb/remember and POST /v1/gb/query.
package gligabao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Environment variables read by NewFromEnv.
const (
	EnvURL    = "GLIGABAO_URL"
	EnvAPIKey = "GLIGABAO_API_KEY"
	EnvUser   = "GLIGABAO_USER"
)

// UserIDHeader is the request header that carries the user ID (WithUserID). The server only takes the user from it (or from the API key).
const UserIDHeader = "gb-user-id"

// DefaultBaseURL is the address of a local gligabao server.
const DefaultBaseURL = "http://127.0.0.1:7878"

// Error is a non-2xx answer of the server (`{"error": "..."}`), or a failure to reach it (Status 0).
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return "gligabao: " + e.Message
	}
	return fmt.Sprintf("gligabao: %d: %s", e.Status, e.Message)
}

// Client talks to a gligabao server. Safe for concurrent use.
type Client struct {
	baseURL string
	apiKey  string
	userID  string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithAPIKey sends the key as "Authorization: Bearer <key>". A server with the auth hook maps it to the user it belongs to
// (and ignores any user ID sent).
func WithAPIKey(key string) Option { return func(c *Client) { c.apiKey = strings.TrimSpace(key) } }

// WithUserID sets the user ID sent with every call, in the "gb-user-id" header. Needed by a server without the auth hook (it
// answers 400 without it); ignored by one with the hook.
func WithUserID(id string) Option { return func(c *Client) { c.userID = id } }

// WithHTTPClient replaces the http.Client (default: no overall timeout, use the context of each call).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithTimeout sets the timeout of the default http.Client. remember can take minutes for a long text.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http = &http.Client{Timeout: d} }
}

// New returns a client for the server at baseURL ("" is DefaultBaseURL).
func New(baseURL string, opts ...Option) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	c := &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 5 * time.Minute}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// NewFromEnv is New with the URL, API key and user ID from GLIGABAO_URL, GLIGABAO_API_KEY and GLIGABAO_USER;
// opts are applied after them.
func NewFromEnv(opts ...Option) *Client {
	base := []Option{WithAPIKey(os.Getenv(EnvAPIKey)), WithUserID(os.Getenv(EnvUser))}
	return New(os.Getenv(EnvURL), append(base, opts...)...)
}

// RememberRequest is the body of POST /v1/gb/remember (text only). See gligabao docs/remember/remember.md.
// Fields left empty are not sent, so the server's defaults apply.
type RememberRequest struct {
	Text          string            `json:"text"`
	Dataset       string            `json:"dataset,omitempty"`       // default "main"
	Kind          string            `json:"kind,omitempty"`          // "knowledge" (default) or "episode"
	ReferenceTime time.Time         `json:"reference_time,omitzero"` // when the text was written; default now
	SessionID     string            `json:"session_id,omitempty"`
	Models        map[string]string `json:"models,omitempty"`    // model per stage
	Reasoning     map[string]string `json:"reasoning,omitempty"` // level per stage: none, low, medium, high
	WebSearch     bool              `json:"web_search,omitempty"`
	ClassifyGraph *bool             `json:"classify_graph,omitempty"` // default true for a text; point to false to turn it off
	GraphClasses  []string          `json:"graph_classes,omitempty"`
	GraphIndex    string            `json:"graph_index,omitempty"`
	// Extra holds any other field of the API (e.g. "timeseries"); it is merged into the body, and wins over the fields above.
	Extra map[string]any `json:"-"`
}

// Entity is an extracted entity.
type Entity struct {
	ID          int64  `json:"id,omitempty"`
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// Edge is a fact (a bi-temporal edge between two entities).
type Edge struct {
	ID          int64      `json:"id"`
	Source      string     `json:"source"`
	Target      string     `json:"target"`
	Relation    string     `json:"relation"`
	Description string     `json:"description,omitempty"`
	ValidAt     time.Time  `json:"valid_at"`
	InvalidAt   *time.Time `json:"invalid_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiredAt   *time.Time `json:"expired_at,omitempty"`
}

// RememberResponse is the answer of remember. Fields of optional parts of the pipeline (chunks, series, geo, ...) are not
// decoded; they are in Raw.
type RememberResponse struct {
	UserID           string    `json:"user_id"`
	Dataset          string    `json:"dataset"`
	Kind             string    `json:"kind"`
	ArtifactID       int64     `json:"artifact_id"`
	SessionID        string    `json:"session_id,omitempty"`
	ValidAt          time.Time `json:"valid_at"`
	CreatedAt        time.Time `json:"created_at"`
	Entities         []Entity  `json:"entities"`
	Edges            []Edge    `json:"edges"`
	InvalidatedEdges []int64   `json:"invalidated_edges"`
	EmbeddedEntities int       `json:"embedded_entities,omitempty"`
	EmbeddedEdges    int       `json:"embedded_edges,omitempty"`
	RememberRunID    int64     `json:"remember_run_id"`

	Raw json.RawMessage `json:"-"` // the whole JSON answer
}

// Remember stores req.Text in memory (POST /v1/gb/remember): the server extracts a knowledge graph from it.
func (c *Client) Remember(ctx context.Context, req RememberRequest) (*RememberResponse, error) {
	if req.Text == "" {
		return nil, &Error{Message: "remember: text is empty"}
	}
	body, err := toMap(req)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Extra {
		body[k] = v
	}
	var out RememberResponse
	if out.Raw, err = c.post(ctx, "/v1/gb/remember", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// QueryRequest is the body of POST /v1/gb/query. See gligabao docs/query/query.md.
type QueryRequest struct {
	Query        string            `json:"query"`
	Dataset      string            `json:"dataset,omitempty"` // default "main"
	SessionID    string            `json:"session_id,omitempty"`
	Reasoning    any               `json:"reasoning,omitempty"` // a level string, or map[string]string per stage
	Models       map[string]string `json:"models,omitempty"`
	GraphIndex   string            `json:"graph_index,omitempty"`
	EvidenceOnly bool              `json:"evidence_only,omitempty"` // only retrieve, no LLM answer
	GeoNearby    bool              `json:"geo_nearby,omitempty"`
	GeoRadiusKm  float64           `json:"geo_radius_km,omitempty"`
	// Extra holds any other field of the API; it is merged into the body, and wins over the fields above.
	Extra map[string]any `json:"-"`
}

// QueryEdge is a fact used as evidence.
type QueryEdge struct {
	Edge
	SourceID   int64 `json:"source_id"`
	TargetID   int64 `json:"target_id"`
	ArtifactID int64 `json:"artifact_id,omitempty"`
}

// Passage is a chunk of a long text used as evidence.
type Passage struct {
	ArtifactID int64  `json:"artifact_id"`
	ParentID   int64  `json:"parent_id"`
	Index      int    `json:"index"`
	Summary    string `json:"summary,omitempty"`
	Text       string `json:"text,omitempty"`
}

// Evidence is what the answer is based on.
type Evidence struct {
	Entities []Entity    `json:"entities"`
	Edges    []QueryEdge `json:"edges"`
	Passages []Passage   `json:"passages"`
}

// QueryResponse is the answer of query.
type QueryResponse struct {
	SessionID  string   `json:"session_id,omitempty"`
	Answer     string   `json:"answer"`
	Evidence   Evidence `json:"evidence"`
	QueryRunID int64    `json:"query_run_id"`

	Raw json.RawMessage `json:"-"` // the whole JSON answer
}

// Query answers req.Query from the knowledge graph (POST /v1/gb/query). A dataset that does not exist is a 404 Error.
func (c *Client) Query(ctx context.Context, req QueryRequest) (*QueryResponse, error) {
	if req.Query == "" {
		return nil, &Error{Message: "query: query is empty"}
	}
	body, err := toMap(req)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Extra {
		body[k] = v
	}
	var out QueryResponse
	if out.Raw, err = c.post(ctx, "/v1/gb/query", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// toMap encodes v with its json tags into a generic map.
func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, &Error{Message: "encode request: " + err.Error()}
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, &Error{Message: "encode request: " + err.Error()}
	}
	return m, nil
}

// post sends body as JSON and decodes the answer into out; it returns the raw answer.
func (c *Client) post(ctx context.Context, path string, body map[string]any, out any) (json.RawMessage, error) {
	delete(body, "user_id") // the server only takes the user from the header
	b, err := json.Marshal(body)
	if err != nil {
		return nil, &Error{Message: "encode request: " + err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, &Error{Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.userID != "" {
		req.Header.Set(UserIDHeader, c.userID)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Message: fmt.Sprintf("cannot reach %s: %v", c.baseURL, err)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &Error{Status: resp.StatusCode, Message: "read answer: " + err.Error()}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return nil, &Error{Status: resp.StatusCode, Message: msg}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, &Error{Status: resp.StatusCode, Message: "decode answer: " + err.Error()}
	}
	return raw, nil
}
