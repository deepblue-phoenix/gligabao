package knowledge

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// verboseLLM wraps an LLM and prints the input and output of every call.
type verboseLLM struct {
	inner memory_core.LLM
	out   io.Writer
	calls int
}

func (v *verboseLLM) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	v.calls++
	n := v.calls
	fmt.Fprintf(v.out, "  ┌─ LLM call #%d input (model=%q max_tokens=%d)\n", n, req.Model, req.MaxTokens)
	if req.System != "" {
		fmt.Fprintf(v.out, "  │ [system]\n%s\n", indent(req.System))
	}
	for _, m := range req.Messages {
		fmt.Fprintf(v.out, "  │ [%s]\n%s\n", m.Role, indent(m.Content))
	}
	resp, err := v.inner.Complete(ctx, req)
	if err != nil {
		fmt.Fprintf(v.out, "  └─ LLM call #%d error: %v\n", n, err)
		return resp, err
	}
	fmt.Fprintf(v.out, "  ├─ LLM call #%d output (model=%s stop=%s in=%d out=%d cost=$%.6f)\n%s\n  └─\n",
		n, resp.Model, resp.StopReason, resp.InputTokens, resp.OutputTokens, resp.CostUSD, indent(resp.Text))
	return resp, nil
}

func indent(s string) string {
	return "  │   " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  │   ")
}
