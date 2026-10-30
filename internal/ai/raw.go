package ai

import (
	"context"
	"encoding/json"
	"fmt"
)

// RawResponse is the response to a Raw call. Content is kept as the API sent it, so it can go back to the model
// unchanged in the next request (needed for tool calling).
type RawResponse struct {
	Model      string
	StopReason string
	Content    []json.RawMessage
	// InputTokens includes prompt-cache tokens; OutputTokens is the generated tokens.
	InputTokens  int
	OutputTokens int
	// CostUSD is computed from token usage and the price table (0 if the model's price is unknown).
	CostUSD float64
}

// Raw sends a Messages API payload as it is (so it can carry tools, tool results, ...) and returns the response.
// "model" and "max_tokens" are filled in from the client's defaults if the payload leaves them out.
func (c *Client) Raw(ctx context.Context, payload map[string]any) (*RawResponse, error) {
	if _, ok := payload["model"]; !ok {
		payload["model"] = c.model
	}
	if _, ok := payload["max_tokens"]; !ok {
		payload["max_tokens"] = c.maxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal request: %w", err)
	}
	data, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	var r struct {
		Model      string            `json:"model"`
		StopReason string            `json:"stop_reason"`
		Content    []json.RawMessage `json:"content"`
		Usage      struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("ai: decode response: %w", err)
	}
	u := r.Usage
	cost, _ := c.prices.Cost(r.Model, Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
	})
	return &RawResponse{
		Model:        r.Model,
		StopReason:   r.StopReason,
		Content:      r.Content,
		InputTokens:  u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens,
		OutputTokens: u.OutputTokens,
		CostUSD:      cost,
	}, nil
}
