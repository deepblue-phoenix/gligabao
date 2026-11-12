package memory_core

import (
	"context"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
)

type warnLLM struct{}

func (warnLLM) Complete(context.Context,
	ai.Request) (*ai.Response, error) {
	return &ai.Response{
		Text:    "ok",
		Model:   "m",
		Warning: "model m ignored the thinking level",
	}, nil
}

// The warning of a call that ran differently from the request (see ai.Response.Warning) is kept, once, in the extra of the trajectory.
func TestTraceKeepsWarnings(t *testing.T) {
	tr := StartTrace("remember", CallOptions{
		Eval: true,
		Dir:  t.TempDir(),
	})
	llm := tr.Wrap(warnLLM{})
	for i := 0; i < 2; i++ {
		if _, err := llm.Complete(context.Background(), ai.Request{
			Messages: []ai.Message{{
				Role:    ai.RoleUser,
				Content: "x",
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	traj := tr.Finish(nil)
	ws, _ := traj.Extra["warnings"].([]string)
	if len(ws) != 1 || ws[0] != "model m ignored the thinking level" {
		t.Fatalf("warnings = %v", traj.Extra["warnings"])
	}
	var nilTrace *Trace
	nilTrace.Warn("ignored") // a nil trace records nothing
}
