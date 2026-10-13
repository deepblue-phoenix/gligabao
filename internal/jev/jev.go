// Package jev wraps calls to Jev, the "system 1" model of TypeSafe: a small, fast model that answers typed questions
// (noul, choice, score) about a state with calibrated probabilities. It is used for quick judgments that precede the
// expensive work of the pipelines (the LLM stages of remember).
//
// The client speaks TypeSafe's evaluation endpoint, POST <base_url>/v1/systemone with a Bearer key (docs:
// https://docs.typesafe.ai/llms.txt). Evaluate is the generic call; Classify is the first use.
package jev

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

const (
	// DefaultBaseURL is the TypeSafe API root.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the flagship model (an alias; the answer reports the concrete version, e.g. "jev-1.13.0").
	DefaultModel = "jev-latest"
	// Provider is the provider name recorded for Jev calls.
	Provider = "jev"

	evalPath       = "/v1/systemone"
	defaultTimeout = 30 * time.Second
)

// Client calls Jev.
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API root (default DefaultBaseURL); useful for tests and proxies.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if u = strings.TrimRight(strings.TrimSpace(u), "/"); u != "" {
			c.baseURL = u
		}
	}
}

// WithModel sets the model of every request (default DefaultModel).
func WithModel(m string) Option {
	return func(c *Client) {
		if m = strings.TrimSpace(m); m != "" {
			c.model = m
		}
	}
}

// WithHTTPClient sets a custom HTTP client (default: 30 s timeout).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// New creates a Client using the given TypeSafe API key (see config.Config).
func New(apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("jev: missing API key (set JEV_API_KEY)")
	}
	c := &Client{apiKey: apiKey, baseURL: DefaultBaseURL, model: DefaultModel, httpClient: &http.Client{Timeout: defaultTimeout}}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

//---------------------------------------------------------------------------------------------

// Question is a typed question about the state. Type is "noul", "choice" or "score"; Instructions and Criteria are
// a string, an object or an array (a map of option to description for a choice, see the API reference).
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Answer is the answer to one question; which fields are set depends on Type.
type Answer struct {
	Type string `json:"type"`
	// Noul is the probability of yes (noul).
	Noul *float64 `json:"noul,omitempty"`
	// Choice is the most probable option (choice).
	Choice string `json:"choice,omitempty"`
	// Score is the probability-weighted level (score).
	Score *float64 `json:"score,omitempty"`
	// Probabilities of each option (choice) or level (score).
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Confidence from 0 to 1 (choice and score).
	Confidence *float64 `json:"confidence,omitempty"`
}

// Response is the result of Evaluate.
type Response struct {
	// Model is the concrete model version that answered.
	Model        string            `json:"model"`
	Answers      map[string]Answer `json:"answers"`
	InputTokens  int               `json:"-"`
	OutputTokens int               `json:"-"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Call is the timing of one call: when it started and how long the round trip took.
type Call struct {
	Started time.Time
	Latency time.Duration
}

// Evaluate asks questions (keyed by ids of your choice) about state (a string, or structured data) in one request.
// The Call is valid even if the returned error is not nil, so latency can be measured for failures too. A call is not
// retried: the latency is the latency of one request.
func (c *Client) Evaluate(ctx context.Context, state any, questions map[string]Question) (*Response, Call, error) {
	start := time.Now()
	resp, err := c.evaluate(ctx, state, questions)
	return resp, Call{Started: start, Latency: time.Since(start)}, err
}

func (c *Client) evaluate(ctx context.Context, state any, questions map[string]Question) (*Response, error) {
	body, err := json.Marshal(struct {
		State     any                 `json:"state"`
		Model     string              `json:"model"`
		Questions map[string]Question `json:"questions"`
	}{state, c.model, questions})
	if err != nil {
		return nil, fmt.Errorf("jev: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+evalPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jev: build request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+c.apiKey)

	hr, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jev: send request: %w", err)
	}
	defer hr.Body.Close()
	data, err := io.ReadAll(hr.Body)
	if err != nil {
		return nil, fmt.Errorf("jev: read response: %w", err)
	}
	if hr.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jev: status %d: %s", hr.StatusCode, truncate(strings.TrimSpace(string(data)), 300))
	}
	var r Response
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("jev: decode response: %w", err)
	}
	r.InputTokens, r.OutputTokens = r.Usage.InputTokens, r.Usage.OutputTokens
	return &r, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
