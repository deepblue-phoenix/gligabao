package atif

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuilderAndWriteFile(t *testing.T) {
	b := NewBuilder(Agent{Name: "a", Version: "1"})
	b.System("sys")
	b.User("hi")
	b.Agent(Step{ModelName: "m", Message: "yo", Metrics: &Metrics{PromptTokens: 10, CompletionTokens: 5, CostUSD: 0.5}})
	b.Tool("looking", "search", map[string]any{"q": "x"}, "found")
	b.Agent(Step{Message: "done", Metrics: &Metrics{PromptTokens: 1, CompletionTokens: 2, CostUSD: 0.25}})

	tr := b.Build()
	if tr.TrajectoryID == "" || tr.TrajectoryID != b.ID() || tr.SessionID != tr.TrajectoryID {
		t.Fatalf("ids: %+v", tr)
	}
	for i, s := range tr.Steps {
		if s.StepID != i+1 || s.Timestamp == "" {
			t.Fatalf("step %d: %+v", i, s)
		}
	}
	tc := tr.Steps[3]
	if tc.ToolCalls[0].ToolCallID != tc.Observation.Results[0].SourceCallID {
		t.Fatalf("tool call not linked to observation: %+v", tc)
	}
	fm := tr.FinalMetrics
	if fm.TotalSteps != 5 || fm.TotalPromptTokens != 11 || fm.TotalCompletionTokens != 7 || fm.TotalCostUSD != 0.75 {
		t.Fatalf("final metrics: %+v", fm)
	}

	dir := filepath.Join(t.TempDir(), "nested", "atif")
	path, err := WriteFile(dir, tr)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, tr.TrajectoryID+".json") {
		t.Fatalf("path %s", path)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "\n  \"schema_version\": \""+SchemaVersion+"\"") {
		t.Fatalf("not pretty-printed:\n%s", data)
	}
	var back Trajectory
	if err := json.Unmarshal(data, &back); err != nil || len(back.Steps) != 5 {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}

func TestNewIDUnique(t *testing.T) {
	if a, b := NewID(), NewID(); a == b || len(a) != 36 {
		t.Fatalf("%s %s", a, b)
	}
}
