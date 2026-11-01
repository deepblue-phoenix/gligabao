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
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
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

// DefaultModel is the LLM model the client uses when a call does not pick one.
func DefaultModel() string { return defaultModel }

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
	// Warning says what the provider made the call do differently from the request, in words for the operator: today, that
	// the model does not take the thinking level asked for, so the call ran without thinking or with the provider's own
	// kind of thinking (see Reasoning for the level it ran with). Empty when the call ran as asked.
	Warning string
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

	// thinkModes remembers, per model, the way of thinking that the provider accepted when it refused the budget (see
	// thinkingFallback), so that the next calls to that model do not fail first. model (string) -> thinkMode.
	thinkModes sync.Map
}

// thinkMode is how a request asks for thinking: a token budget (the Claude 3.7 to 4.5 way), the provider's adaptive thinking
// steered by an effort (newer models, which refuse the budget), or not at all.
type thinkMode struct {
	kind string // thinkBudget, thinkAdaptive or thinkOff
	// why is what the provider said when it refused the budget (empty for thinkBudget).
	why string
}

const (
	thinkBudget   = "budget"
	thinkAdaptive = "adaptive"
	thinkOff      = "off"
)

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

	level, _ := NormalizeReasoning(req.Reasoning)
	budget := ReasoningBudget(level)
	var warning string
	mode := thinkMode{kind: thinkBudget}
	switch {
	case budget == 0:
		level, mode.kind = ReasoningNone, thinkOff
	case !SupportsReasoning(model):
		warning = fmt.Sprintf("model %s does not support thinking: the thinking level %q was ignored and the call ran without thinking", model, level)
		level, mode.kind = ReasoningNone, thinkOff
	default:
		if m, ok := c.thinkModes.Load(model); ok {
			mode = m.(thinkMode)
		}
	}

	var data []byte
	for {
		body, err := json.Marshal(c.apiRequestFor(model, maxTokens, req, level, budget, mode.kind))
		if err != nil {
			return nil, fmt.Errorf("ai: marshal request: %w", err)
		}
		var postErr error
		if data, postErr = c.post(ctx, body); postErr == nil {
			break
		}
		// The provider refuses the way of thinking asked for (e.g. "thinking.type.enabled is not supported for this model"):
		// instead of failing the call, ask in the way it says it takes, or give up thinking, and say so in the response.
		next, why := thinkingFallback(postErr, mode.kind)
		if next == "" {
			return nil, postErr
		}
		mode = thinkMode{kind: next, why: why}
		c.thinkModes.Store(model, mode)
		log.WithFields(log.Fields{"model": model, "reasoning": level, "thinking": next, "provider_said": why}).Warn("model refused the thinking request; retrying")
	}
	switch mode.kind {
	case thinkAdaptive:
		warning = fmt.Sprintf("model %s does not take a thinking budget: the thinking level %q ran as adaptive thinking with effort %q (provider said: %s)", model, level, effortOf(level), mode.why)
	case thinkOff:
		if budget > 0 && warning == "" {
			warning = fmt.Sprintf("model %s does not support the thinking level %q: the call ran without thinking (provider said: %s)", model, level, mode.why)
		}
		level = ReasoningNone
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
		Warning:      warning,
	}, nil
}

// apiRequestFor builds the Messages API request of a call that asks for thinking in the given way (see thinkMode). The thinking
// budget is added to max_tokens, as thinking is spent out of it, so the answer keeps its size.
func (c *Client) apiRequestFor(model string, maxTokens int, req Request, level string, budget int, kind string) apiRequest {
	ar := apiRequest{Model: model, MaxTokens: maxTokens, System: req.System, Messages: req.Messages}
	switch kind {
	case thinkBudget:
		ar.MaxTokens = maxTokens + budget
		ar.Thinking = &thinking{Type: "enabled", BudgetTokens: budget}
	case thinkAdaptive:
		ar.MaxTokens = maxTokens + budget
		ar.Thinking = &thinking{Type: "adaptive"}
		ar.OutputConfig = &outputConfig{Effort: effortOf(level)}
	}
	return ar
}

// effortOf is the effort of Anthropic's adaptive thinking for a reasoning level ("low", "medium" or "high").
func effortOf(level string) string {
	if level == ReasoningMedium || level == ReasoningHigh {
		return level
	}
	return ReasoningLow
}

// APIError is an error answer of the Anthropic API.
type APIError struct {
	Status  int
	Type    string // e.g. "invalid_request_error"; empty if the body was not an API error
	Message string
}

func (e *APIError) Error() string {
	if e.Type == "" {
		return fmt.Sprintf("ai: anthropic status %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("ai: anthropic %d %s: %s", e.Status, e.Type, e.Message)
}

// thinkingFallback says how to retry a call that failed with err when it asked for thinking in the given way: the way to ask
// next (thinkAdaptive when the provider says it takes that, thinkOff otherwise) and what the provider said, or "" if err is not a
// refusal of the thinking request (or there is nothing left to try).
func thinkingFallback(err error, kind string) (next, why string) {
	var ae *APIError
	if kind == thinkOff || !errors.As(err, &ae) || ae.Status != http.StatusBadRequest {
		return "", ""
	}
	msg := strings.ToLower(ae.Message)
	if !strings.Contains(msg, "thinking") || !(strings.Contains(msg, "not supported") || strings.Contains(msg, "adaptive")) {
		return "", ""
	}
	if kind == thinkBudget && strings.Contains(msg, "thinking.type.adaptive") {
		return thinkAdaptive, ae.Message
	}
	return thinkOff, ae.Message
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
			return nil, &APIError{Status: resp.StatusCode, Type: e.Error.Type, Message: e.Error.Message}
		}
		return nil, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
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
	// OutputConfig steers adaptive thinking (see thinkAdaptive).
	OutputConfig *outputConfig `json:"output_config,omitempty"`
}

type thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type outputConfig struct {
	Effort string `json:"effort"`
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
