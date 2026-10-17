// Package ai provides a simple LLM wrapper. For now it only calls the Anthropic Messages API.
package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

//---------------------------------------------------------------------------------------------

const (
	defaultBaseURL   = "https://api.anthropic.com"
	defaultModel     = "claude-haiku-5-5"
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
	// Images are attached to the message (user turns only). They are sent before Content as image blocks.
	Images []Image `json:"-"`
}

// Image is an image attached to a Message.
type Image struct {
	// MediaType is image/jpeg, image/png, image/gif or image/webp.
	MediaType string
	// Data is the raw (not base64) image.
	Data []byte
}

// MarshalJSON encodes the message the way the Messages API expects: a plain string content, or, when there are
// images, an array of content blocks (the images, then the text).
func (m Message) MarshalJSON() ([]byte, error) {
	if len(m.Images) == 0 {
		return json.Marshal(struct {
			Role    Role   `json:"role"`
			Content string `json:"content"`
		}{m.Role, m.Content})
	}
	blocks := make([]contentBlock, 0, len(m.Images)+1)
	for _, im := range m.Images {
		blocks = append(blocks, contentBlock{Type: "image", Source: &imageSource{
			Type: "base64", MediaType: im.MediaType, Data: base64.StdEncoding.EncodeToString(im.Data),
		}})
	}
	if m.Content != "" {
		blocks = append(blocks, contentBlock{Type: "text", Text: m.Content})
	}
	return json.Marshal(struct {
		Role    Role           `json:"role"`
		Content []contentBlock `json:"content"`
	}{m.Role, blocks})
}

// Request describes a single LLM call. Zero values fall back to client defaults.
type Request struct {
	System    string
	Messages  []Message
	Model     string
	MaxTokens int
	// Reasoning is the reasoning (extended thinking) level: ReasoningNone (or ""), ReasoningLow, ReasoningMedium or
	// ReasoningHigh. See ReasoningBudget. MaxTokens is the size of the answer: the thinking budget is added to it.
	Reasoning string
}

// The reasoning levels of a Request.
const (
	ReasoningNone   = "none"
	ReasoningLow    = "low"
	ReasoningMedium = "medium"
	ReasoningHigh   = "high"
)

// ReasoningLevels lists the valid reasoning levels, from none to the most.
var ReasoningLevels = []string{ReasoningNone, ReasoningLow, ReasoningMedium, ReasoningHigh}

// NormalizeReasoning trims and lowercases a reasoning level; "" means ReasoningNone. ok is false for an unknown one.
func NormalizeReasoning(level string) (string, bool) {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return ReasoningNone, true
	}
	for _, l := range ReasoningLevels {
		if l == level {
			return level, true
		}
	}
	return "", false
}

// SupportsReasoning says whether the model supports extended thinking. The Claude 3 and 3.5 families do not (a request
// with a thinking budget to them fails), so Complete leaves the budget out for them and reports ReasoningNone in
// Response.Reasoning. Unknown models are assumed to support it.
func SupportsReasoning(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return !strings.HasPrefix(m, "claude-3-") && !strings.HasPrefix(m, "claude-2") && !strings.HasPrefix(m, "claude-instant")
}

// ReasoningBudget is the extended thinking budget (in tokens) of a reasoning level: 0 for none or an unknown level.
func ReasoningBudget(level string) int {
	switch level {
	case ReasoningLow:
		return 1024
	case ReasoningMedium:
		return 4096
	case ReasoningHigh:
		return 16384
	}
	return 0
}

// Response is the result of an LLM call.
type Response struct {
	Text       string
	Provider   string
	Model      string
	StopReason string
	// InputTokens includes prompt-cache read/write tokens; OutputTokens is the generated tokens.
	InputTokens  int
	OutputTokens int
	// CostUSD is computed from token usage and the price table (0 if the model's price is unknown).
	CostUSD float64
	// Reasoning is the reasoning level the call actually ran with (ReasoningNone if it did not reason, e.g. because the
	// model does not support it). Empty from an LLM that does not report it.
	Reasoning string
	// InferenceID is the ID of the gb_inferences row that records this call. Set by memory_core.WithRecording, 0 if the
	// call was not recorded.
	InferenceID int64
}

//---------------------------------------------------------------------------------------------

// Client calls the LLM.
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	maxTokens  int
	prices     *PriceTable
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithModel sets the default model.
func WithModel(m string) Option { return func(c *Client) { c.model = m } }

// WithMaxTokens sets the default max output tokens.
func WithMaxTokens(n int) Option { return func(c *Client) { c.maxTokens = n } }

// WithPrices sets the price table used to compute call costs (default: DefaultPrices).
func WithPrices(p *PriceTable) Option { return func(c *Client) { c.prices = p } }

// WithBaseURL overrides the API base URL (useful for tests/proxies).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// New creates a Client using the given Anthropic API key (see config.Config).
func New(apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("ai: missing API key (set ANTHROPIC_API_KEY)")
	}
	c := &Client{
		apiKey:     apiKey,
		baseURL:    defaultBaseURL,
		model:      defaultModel,
		maxTokens:  defaultMaxTokens,
		prices:     NewPriceTable(nil),
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

	ar0 := apiRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    req.System,
		Messages:  req.Messages,
	}
	level, _ := NormalizeReasoning(req.Reasoning)
	if b := ReasoningBudget(level); b > 0 && SupportsReasoning(model) {
		// The budget is spent out of max_tokens, which must stay above it: it is added so the answer keeps its size.
		ar0.MaxTokens = maxTokens + b
		ar0.Thinking = &thinking{Type: "enabled", BudgetTokens: b}
	} else {
		level = ReasoningNone
	}
	body, err := json.Marshal(ar0)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal request: %w", err)
	}

	data, err := c.post(ctx, body)
	if err != nil {
		return nil, err
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
	u := ar.Usage
	cost, _ := c.prices.Cost(ar.Model, Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
	})
	return &Response{
		Text:         sb.String(),
		Provider:     ProviderAnthropic,
		Model:        ar.Model,
		StopReason:   ar.StopReason,
		InputTokens:  u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens,
		OutputTokens: u.OutputTokens,
		CostUSD:      cost,
		Reasoning:    level,
	}, nil
}

// post sends a Messages API request body and returns the body of the 200 response; any other status is an error that
// carries Anthropic's message.
func (c *Client) post(ctx context.Context, body []byte) ([]byte, error) {
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
	return data, nil
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
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Thinking  *thinking `json:"thinking,omitempty"`
}

type thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type contentBlock struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *imageSource `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type apiResponse struct {
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}
