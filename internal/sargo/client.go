// Package sargo is a client for sargo (rust/sargo), the small model request router: one provider-neutral inference request in,
// one provider-neutral reply out, for Anthropic or OpenAI. The wire format is rust/sargo/inference.schema.json.
//
// sargo is stateless, so the caller runs the tool-calling loop: while the reply's StopReason is StopToolUse, run the tools, append
// Response.AssistantMessage() and a user message with one ToolResultBlock per tool call to the conversation, and call Infer again.
package sargo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxReplyBytes caps how much of a reply is read.
const maxReplyBytes = 64 << 20

// defaultTimeout bounds a whole call made with the default HTTP client.
const defaultTimeout = 5 * time.Minute

// Client talks to one sargo server. It is safe for concurrent use.
type Client struct {
	baseURL string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient makes the client use hc instead of its default (which has a 5 minute timeout).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

// New returns a client for the sargo server at baseURL (e.g. "http://127.0.0.1:8080").
func New(baseURL string, opts ...Option) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("sargo: base URL is empty")
	}
	c := &Client{baseURL: baseURL, http: &http.Client{Timeout: defaultTimeout}}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// APIError is an error reply from the sargo server (ErrorResponse in the schema).
type APIError struct {
	// Status is the HTTP status: 400 bad request (do not retry as is), 404 unknown path, 500 internal error, 502 the model
	// provider failed (may be retried), 503 the provider is not configured in sargo.
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("sargo: HTTP %d: %s", e.Status, e.Message)
}

// Retryable reports whether trying the same request again may work (the model provider failed).
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusBadGateway
}

// Infer runs one model inference (POST /v1/infer). The request is validated first (see Request.Validate). A non-200 reply is
// returned as an *APIError.
func (c *Client) Infer(ctx context.Context, req *Request) (*Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("sargo: marshal request: %w", err)
	}
	data, err := c.do(ctx, http.MethodPost, "/v1/infer", body)
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("sargo: decode reply: %w", err)
	}
	return &resp, nil
}

// Health calls GET /health and returns nil when the server answers "ok".
func (c *Client) Health(ctx context.Context) error {
	data, err := c.do(ctx, http.MethodGet, "/health", nil)
	if err != nil {
		return err
	}
	var h HealthResponse
	if err := json.Unmarshal(data, &h); err != nil {
		return fmt.Errorf("sargo: decode health reply: %w", err)
	}
	if h.Status != "ok" {
		return fmt.Errorf("sargo: health status %q", h.Status)
	}
	return nil
}

// do sends the request and returns the body of a 200 reply. The body is sent with a Content-Length (sargo does not accept chunked).
func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return nil, fmt.Errorf("sargo: build request: %w", err)
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("Accept", "application/json")
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sargo: %s %s: %w", method, path, err)
	}
	defer httpResp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(httpResp.Body, maxReplyBytes))
	if err != nil {
		return nil, fmt.Errorf("sargo: read reply: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, newAPIError(httpResp.StatusCode, data)
	}
	return data, nil
}

// newAPIError builds the error of a non-200 reply; if the body is not an ErrorResponse its text is used as the message.
func newAPIError(status int, data []byte) *APIError {
	var er ErrorResponse
	if err := json.Unmarshal(data, &er); err == nil && er.Error != "" {
		return &APIError{Status: status, Message: er.Error}
	}
	msg := strings.TrimSpace(string(data))
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &APIError{Status: status, Message: msg}
}
