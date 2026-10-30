package knowledge

import (
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
)

// TestTrajectoriesJSONBudget checks that long strings are cut, progressively, until the trajectories fit the budget, and that
// small trajectories are sent whole.
func TestTrajectoriesJSONBudget(t *testing.T) {
	b := atif.NewBuilder(atif.Agent{Name: "gligabao-remember", Version: "1"})
	b.User(strings.Repeat("é", 50_000)) // multi-byte: a cut must not split a character
	b.User("short")
	traj := b.Build()

	whole := trajectoriesJSON([]*atif.Trajectory{traj}, 1_000_000)
	if len(whole) != 1 || strings.Contains(whole[0], "bytes cut") {
		t.Fatalf("a trajectory within the budget was cut")
	}
	cut := trajectoriesJSON([]*atif.Trajectory{traj}, 30_000)
	if len(cut) != 1 || len(cut[0]) > 30_000 || !strings.Contains(cut[0], "bytes cut]") || !strings.Contains(cut[0], "short") {
		t.Fatalf("cut trajectory: %d bytes", len(cut[0]))
	}
	if strings.ContainsRune(cut[0], '\uFFFD') {
		t.Error("a cut split a multi-byte character")
	}
	if got := trajectoriesJSON(nil, 10); got != nil {
		t.Errorf("no trajectories: %v", got)
	}
}
