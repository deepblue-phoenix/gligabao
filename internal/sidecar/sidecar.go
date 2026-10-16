// Package sidecar is the client the Go server uses to talk to the Python sidecar (side_car/main_sidecar.py) over HTTP.
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

// DefaultBaseURL is where the sidecar listens by default (SIDECAR_PORT in side_car/main_sidecar.py).
const DefaultBaseURL = "http://127.0.0.1:8787"

// Client calls the sidecar's HTTP API.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client for the sidecar at baseURL (DefaultBaseURL when empty).
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
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
		return fmt.Errorf("sidecar GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("sidecar GET %s: decode: %w", path, err)
	}
	return nil
}
