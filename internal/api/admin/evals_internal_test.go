package admin

import (
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/experiments"
)

func TestHistoryPoints(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	runs := []experiments.Run{{ID: 1, StartedAt: t0}, {ID: 2, StartedAt: t0.Add(time.Hour)}}
	results := []experiments.Result{
		{RunID: 1, Status: "failed", Problems: []string{"x"}, Answer: "a", CostUSD: 1, Steps: 3},
		{RunID: 9, Status: "passed"}, // run not in the experiment's runs
		{RunID: 2, Status: "passed"},
	}
	got := historyPoints(results, runs)
	if len(got) != 2 || got[0].RunID != 1 || !got[0].RunStartedAt.Equal(t0) || got[0].Steps != 3 || got[1].RunID != 2 ||
		!got[1].RunStartedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("points: %+v", got)
	}
	if historyPoints(nil, runs) == nil {
		t.Fatal("empty history must be an empty list, not nil")
	}
}
