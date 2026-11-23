package ml_sidecar

import (
	"context"
	"errors"
	"strings"
)

//---------------------------------------------------------------------------

// InferRequest is the body of POST /infer/query.
type InferRequest struct {
	Prompt string `json:"prompt"`
	// ModelSize is "4b", "12b" or "31b" (default "4b"): it picks the hardware, and must be the size the model was trained for.
	ModelSize string `json:"model_size,omitempty"`
	// ModelPath picks the weights: the Path of a trained model ("trained/<job id>"). Without it the ML sidecar uses the
	// adapter that older versions saved per model size.
	ModelPath   string   `json:"model_path,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

// InferResponse is what POST /infer/query answers.
type InferResponse struct {
	Response  string `json:"response"`
	ModelSize string `json:"model_size"`
	ModelPath string `json:"model_path"`
	// InputTokens is the size of the prompt as the model saw it (chat template and special tokens included), OutputTokens
	// the number of tokens it generated. 0 when the ML sidecar (an older deployment) does not report them.
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// ColdStart is whether this request loaded the model (its container was not up with it); LoadSeconds how long that took.
	// Nil / 0 when the ML sidecar (an older deployment) does not report it.
	ColdStart   *bool   `json:"cold_start,omitempty"`
	LoadSeconds float64 `json:"load_seconds,omitempty"`
	Error       string  `json:"error,omitempty"`
}

//---------------------------------------------------------------------------

// InferError is the error of a generation that failed on the ML sidecar (it answered 200 with an "error"), e.g. because the
// weights are not on its volume. Message is the ML sidecar's text.
type InferError struct{ Message string }

func (e *InferError) Error() string { return "ML sidecar: " + e.Message }

// Infer runs a prompt through a fine-tuned model on the ML sidecar (POST /infer/query). The call takes as long as the model
// needs to load (minutes on a cold start), so only ctx limits it. A failure of the generation itself
// (e.g. the weights are not on the volume) is returned as an error with the ML sidecar's message.
func (c *Client) Infer(ctx context.Context,
	req InferRequest) (InferResponse, error) {
	var out InferResponse
	if strings.TrimSpace(req.Prompt) == "" {
		return out, errors.New("ML sidecar: prompt is required")
	}
	if err := c.postVia(ctx,
		c.stream,
		"/infer/query",
		req,
		&out); err != nil {
		return out, err
	}
	if out.Error != "" {
		return out, &InferError{
			Message: out.Error,
		}
	}
	return out, nil
}
