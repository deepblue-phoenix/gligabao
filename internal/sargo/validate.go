package sargo

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

var toolNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Validate checks the request against the rules of rust/sargo/inference.schema.json, so a bad request fails here, not with a
// 400 from the server. Infer calls it before sending.
func (r *Request) Validate() error {
	if r == nil {
		return errors.New("sargo: nil request")
	}
	switch r.Provider {
	case ProviderAnthropic, ProviderOpenAI:
	default:
		return fmt.Errorf("sargo: unknown provider %q", r.Provider)
	}
	if r.Model == "" {
		return errors.New("sargo: model is required")
	}
	if len(r.Messages) == 0 {
		return errors.New("sargo: at least one message is required")
	}
	if r.Temperature != nil && *r.Temperature < 0 {
		return errors.New("sargo: temperature must not be negative")
	}
	for i, m := range r.Messages {
		if m.Role != RoleUser && m.Role != RoleAssistant {
			return fmt.Errorf("sargo: messages[%d]: unknown role %q", i, m.Role)
		}
		for j, b := range m.Content {
			if err := b.validate(); err != nil {
				return fmt.Errorf("sargo: messages[%d].content[%d]: %w", i, j, err)
			}
		}
	}
	for i, t := range r.Tools {
		if err := t.validate(); err != nil {
			return fmt.Errorf("sargo: tools[%d]: %w", i, err)
		}
	}
	return nil
}

func (b ContentBlock) validate() error {
	switch b.Type {
	case BlockText, BlockToolResult:
		return nil
	case BlockToolUse:
		if len(b.Input) == 0 {
			return nil // marshalled as {}
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(b.Input, &obj); err != nil || obj == nil {
			return errors.New("tool_use input must be a JSON object")
		}
		return nil
	}
	return fmt.Errorf("unknown block type %q", b.Type)
}

func (t Tool) validate() error {
	if t.Name == "" || len(t.Name) > 64 || !toolNameRe.MatchString(t.Name) {
		return fmt.Errorf("invalid tool name %q (letters, digits, _ and -, 1 to 64 characters)", t.Name)
	}
	if t.InputSchema["type"] != "object" {
		return fmt.Errorf("tool %q: input_schema must be an object schema (\"type\": \"object\")", t.Name)
	}
	return nil
}
