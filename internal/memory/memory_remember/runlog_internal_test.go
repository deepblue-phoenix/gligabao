package memory_remember

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

// An output over maxStageOutputBytes is replaced by a stub; a smaller one is kept whole.
func TestRunLogOutputCap(t *testing.T) {
	ctx := context.Background()
	d, err := relational.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	run := startRunLog(ctx, d, 0, "u", "main", "knowledge", nil)
	run.Output(StageStore, "small", "", map[string]any{"text": "ok"})
	run.Output(StageStore, "big", "", map[string]any{"text": strings.Repeat("é", maxStageOutputBytes)})
	got, err := relational.ListRememberStageOutputs(ctx, d, run.ID(), StageStore)
	if err != nil || len(got) != 2 {
		t.Fatalf("outputs: %+v, %v", got, err)
	}
	if string(got[0].Output) != `{"text":"ok"}` {
		t.Errorf("small output: %s", got[0].Output)
	}
	var stub struct {
		Truncated bool
		Bytes     int
		Preview   string
	}
	if err := json.Unmarshal(got[1].Output, &stub); err != nil || !stub.Truncated || stub.Bytes <= maxStageOutputBytes || stub.Preview == "" || len(got[1].Output) > 8<<10 {
		t.Errorf("big output: %v, %+v (%d bytes stored)", err, stub, len(got[1].Output))
	}
}
