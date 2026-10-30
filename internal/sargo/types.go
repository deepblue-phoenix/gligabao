package sargo

import (
	"encoding/json"
	"fmt"
)

// The types in this file mirror rust/sargo/inference.schema.json (JSON Schema draft 2020-12). Keep them in step with it;
// sargo_test.go validates what they marshal to against that file.

// Provider names a model provider sargo can forward to.
type Provider string

// The providers sargo supports.
const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
)

// Role is the author of a Message.
type Role string

// The roles of a Message. Tool results go in a user message; tool calls come back in an assistant message.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Block types (the "type" field of a content block).
const (
	BlockText       = "text"
	BlockToolUse    = "tool_use"
	BlockToolResult = "tool_result"
)

// Stop reasons sargo normalises across providers. Any other value is the provider's raw one, so callers must tolerate it.
const (
	StopEndTurn   = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
)

// DefaultMaxTokens is what sargo uses when a request leaves MaxTokens out.
const DefaultMaxTokens = 1024

// Request is one model inference (InferenceRequest in the schema). It is provider-neutral.
type Request struct {
	Provider Provider `json:"provider"`
	// Model is the provider's own model name, passed through untouched.
	Model string `json:"model"`
	// System is the optional system prompt ("" = none).
	System string `json:"system,omitempty"`
	// Messages is the whole conversation so far, oldest first (sargo is stateless).
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	// MaxTokens is the max tokens to generate (0 = sargo's default, DefaultMaxTokens).
	MaxTokens uint32 `json:"max_tokens,omitempty"`
	// Temperature is the sampling temperature (nil = the provider's default).
	Temperature *float64 `json:"temperature,omitempty"`
}

// Message is one turn of the conversation.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// MarshalJSON makes sure Content is always an array: the schema accepts neither null nor a plain string.
func (m Message) MarshalJSON() ([]byte, error) {
	content := m.Content
	if content == nil {
		content = []ContentBlock{}
	}
	return json.Marshal(struct {
		Role    Role           `json:"role"`
		Content []ContentBlock `json:"content"`
	}{m.Role, content})
}

// ContentBlock is a piece of a message, discriminated by Type. Which fields are used depends on Type:
//
//	text:        Text
//	tool_use:    ID, Name, Input
//	tool_result: ToolUseID, Content, IsError
//
// Build one with TextBlock or ToolResultBlock.
type ContentBlock struct {
	Type string

	// Text is the text of a "text" block.
	Text string

	// ID, Name and Input are the fields of a "tool_use" block: the model asks the caller to run tool Name with the arguments
	// Input (a JSON object).
	ID    string
	Name  string
	Input json.RawMessage

	// ToolUseID, Content and IsError are the fields of a "tool_result" block: the output of the tool call whose id is ToolUseID.
	ToolUseID string
	Content   string
	IsError   bool
}

// TextBlock returns a "text" block.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: BlockText, Text: text}
}

// ToolResultBlock returns a "tool_result" block answering the tool_use block with the given id.
func ToolResultBlock(toolUseID, content string, isError bool) ContentBlock {
	return ContentBlock{Type: BlockToolResult, ToolUseID: toolUseID, Content: content, IsError: isError}
}

// UserText returns a user message with one text block.
func UserText(text string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{TextBlock(text)}}
}

// MarshalJSON writes only the fields that belong to the block's type (the schema forbids any other property).
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case BlockText:
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{b.Type, b.Text})
	case BlockToolUse:
		input := b.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		return json.Marshal(struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{b.Type, b.ID, b.Name, input})
	case BlockToolResult:
		return json.Marshal(struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			Content   string `json:"content"`
			IsError   bool   `json:"is_error,omitempty"`
		}{b.Type, b.ToolUseID, b.Content, b.IsError})
	}
	return nil, fmt.Errorf("sargo: unknown content block type %q", b.Type)
}

// UnmarshalJSON reads a block by its "type".
func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("sargo: content block: %w", err)
	}
	*b = ContentBlock{Type: head.Type}
	switch head.Type {
	case BlockText:
		var v struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return fmt.Errorf("sargo: text block: %w", err)
		}
		b.Text = v.Text
	case BlockToolUse:
		var v struct {
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return fmt.Errorf("sargo: tool_use block: %w", err)
		}
		b.ID, b.Name, b.Input = v.ID, v.Name, v.Input
	case BlockToolResult:
		var v struct {
			ToolUseID string `json:"tool_use_id"`
			Content   string `json:"content"`
			IsError   bool   `json:"is_error"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return fmt.Errorf("sargo: tool_result block: %w", err)
		}
		b.ToolUseID, b.Content, b.IsError = v.ToolUseID, v.Content, v.IsError
	default:
		return fmt.Errorf("sargo: unknown content block type %q", head.Type)
	}
	return nil
}

// Tool is a tool the model may call.
type Tool struct {
	// Name has letters, digits, underscore and hyphen only, at most 64 characters.
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// InputSchema is the JSON Schema of the tool's arguments; it must be an object schema ({"type":"object", ...}).
	InputSchema map[string]any `json:"input_schema"`
}

// Response is the 200 reply of POST /v1/infer (InferenceResponse in the schema).
type Response struct {
	// Provider is the provider that answered.
	Provider Provider `json:"provider"`
	// Model is the model name the provider reports (may be more specific than the requested one).
	Model string `json:"model"`
	// Content is the answer: text and/or tool_use blocks.
	Content []ContentBlock `json:"content"`
	// StopReason is StopEndTurn, StopToolUse, StopMaxTokens or the provider's raw value.
	StopReason string `json:"stop_reason"`
	Usage      Usage  `json:"usage"`
}

// Usage is the token usage of a call.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Text returns the text blocks of the answer joined together.
func (r *Response) Text() string {
	var s string
	for _, b := range r.Content {
		if b.Type == BlockText {
			s += b.Text
		}
	}
	return s
}

// ToolUses returns the tool_use blocks of the answer, in order.
func (r *Response) ToolUses() []ContentBlock {
	var uses []ContentBlock
	for _, b := range r.Content {
		if b.Type == BlockToolUse {
			uses = append(uses, b)
		}
	}
	return uses
}

// AssistantMessage returns the answer as an assistant message, unchanged: the first message to append to the conversation when
// the model asked for tools (StopReason == StopToolUse). Follow it with a user message holding one tool_result block per tool_use.
func (r *Response) AssistantMessage() Message {
	return Message{Role: RoleAssistant, Content: r.Content}
}

// ErrorResponse is the body of every error reply (ErrorResponse in the schema).
type ErrorResponse struct {
	Error string `json:"error"`
}

// HealthResponse is the 200 reply of GET /health (HealthResponse in the schema).
type HealthResponse struct {
	Status string `json:"status"`
}
