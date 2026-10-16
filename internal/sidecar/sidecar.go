// Package sidecar is the client the Go server uses to talk to the Python sidecar (sidecar/main_sidecar.py) over HTTP.
package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is where the sidecar listens by default (SIDECAR_PORT in sidecar/main_sidecar.py).
const DefaultBaseURL = "http://127.0.0.1:8787"

// Client calls the sidecar's HTTP API.
type Client struct {
	baseURL string
	http    *http.Client
	// stream is for the responses that stay open (SSE): it has no overall timeout; the caller's context ends them.
	stream *http.Client
}

// StatusError is the error for a sidecar response with a status other than 200. Body is the start of the response body.
type StatusError struct {
	Method, Path string
	Code         int
	Body         string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("sidecar %s %s: status %d: %s", e.Method, e.Path, e.Code, e.Body)
}

// Message is the sidecar's error message: the "error" field of its JSON body, or the body itself.
func (e *StatusError) Message() string {
	var b struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(e.Body), &b) == nil && b.Error != "" {
		return b.Error
	}
	return e.Body
}

// New returns a Client for the sidecar at baseURL (DefaultBaseURL when empty).
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
		stream:  &http.Client{},
	}
}

// HealthResponse is the body of GET /health.
type HealthResponse struct {
	Status string `json:"status"`
}

// Health checks that the sidecar is up and healthy.
func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	var out HealthResponse
	if err := c.get(ctx, "/health", &out); err != nil {
		return out, err
	}
	if out.Status != "ok" {
		return out, fmt.Errorf("sidecar unhealthy: status %q", out.Status)
	}
	return out, nil
}

// BackendStatus is one entry of GET /backends: "name", "configured" and "healthy" (bool) are always there; a backend adds
// its own fields (the modal one: "deployed" (bool, null if unknown), "app", and "error" when it is unhealthy).
type BackendStatus map[string]any

// Backends lists the backends the sidecar offers for training, with their live status.
func (c *Client) Backends(ctx context.Context) ([]BackendStatus, error) {
	var out struct {
		Backends []BackendStatus `json:"backends"`
	}
	if err := c.get(ctx, "/backends", &out); err != nil {
		return nil, err
	}
	return out.Backends, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sidecar GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Method: http.MethodGet, Path: path, Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("sidecar GET %s: decode: %w", path, err)
	}
	return nil
}
