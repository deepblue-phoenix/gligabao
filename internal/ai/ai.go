// Package ai provides a simple LLM wrapper. For now it only calls the Anthropic Messages API.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

//---------------------------------------------------------------------------------------------

const (
	defaultBaseURL   = "https://api.anthropic.com"
	defaultModel     = "claude-haiku-4-5"
	defaultMaxTokens = 1024
	apiVersion       = "2023-06-01"
)

//---------------------------------------------------------------------------------------------

// Role is the author of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is a single conversation turn.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// Request describes a single LLM call. Zero values fall back to client defaults.
type Request struct {
	System      string
	Messages    []Message
	Model       string
	MaxTokens   int
	Temperature *float64
}

// Response is the result of an LLM call.
type Response struct {
	Text         string
	Model        string
	StopReason   string
	InputTokens  int
	OutputTokens int
}

//---------------------------------------------------------------------------------------------

// Client calls the LLM.
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	maxTokens  int
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithModel sets the default model.
func WithModel(m string) Option { return func(c *Client) { c.model = m } }

// WithMaxTokens sets the default max output tokens.
func WithMaxTokens(n int) Option { return func(c *Client) { c.maxTokens = n } }

// WithBaseURL overrides the API base URL (useful for tests/proxies).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// New creates a Client. If apiKey is empty, ANTHROPIC_API_KEY is used.
func New(apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil, errors.New("ai: missing API key (set ANTHROPIC_API_KEY)")
	}
	c := &Client{
		apiKey:     apiKey,
		baseURL:    defaultBaseURL,
		model:      defaultModel,
		maxTokens:  defaultMaxTokens,
		httpClient: &http.Client{Timeout: 2 * time.Minute},
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

//---------------------------------------------------------------------------------------------

// Complete sends a request and returns the model's text response.
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("ai: no messages")
	}
	model := req.Model
	if model == "" {
		model = c.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = c.maxTokens
	}

	body, err := json.Marshal(apiRequest{
		Model:       model,
		MaxTokens:   maxTokens,
		System:      req.System,
		Messages:    req.Messages,
		Temperature: req.Temperature,
	})
	if err != nil {
		return nil, fmt.Errorf("ai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ai: build request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: send request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ai: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e apiError
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("ai: anthropic %d %s: %s", resp.StatusCode, e.Error.Type, e.Error.Message)
		}
		return nil, fmt.Errorf("ai: anthropic status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var ar apiResponse
	if err := json.Unmarshal(data, &ar); err != nil {
		return nil, fmt.Errorf("ai: decode response: %w", err)
	}
	var sb strings.Builder
	for _, b := range ar.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return &Response{
		Text:         sb.String(),
		Model:        ar.Model,
		StopReason:   ar.StopReason,
		InputTokens:  ar.Usage.InputTokens,
		OutputTokens: ar.Usage.OutputTokens,
	}, nil
}

// Ask is a convenience for a single-prompt call.
func (c *Client) Ask(ctx context.Context, prompt string) (string, error) {
	r, err := c.Complete(ctx, Request{Messages: []Message{{Role: RoleUser, Content: prompt}}})
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

//---------------------------------------------------------------------------------------------
// Wire types.

type apiRequest struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	System      string    `json:"system,omitempty"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
}

type apiResponse struct {
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}
