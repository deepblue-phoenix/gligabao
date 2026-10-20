package admin

import (
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals_neo/db"
)

func TestHistoryPoints(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	runs := []core.ExperimentRun{{ID: 1, Timing: core.Timing{StartedAt: &t0}}, {ID: 2, Timing: core.Timing{StartedAt: &t1}}}
	done := func(verdict core.Verdict, meta *core.KnowledgeCaseRunMeta) core.CaseRun {
		return core.CaseRun{Status: core.StatusCompleted, Verdict: verdict, CostUSD: 1, Meta: meta}
	}
	results := []evalsdb.CaseHistoryEntry{
		{ExperimentRunID: 1, CaseRun: done(core.VerdictFail, &core.KnowledgeCaseRunMeta{Problems: []string{"x"}, Answer: "a", Steps: 3})},
		{ExperimentRunID: 9, CaseRun: done(core.VerdictPass, nil)}, // run not in the experiment's runs
		{ExperimentRunID: 2, CaseRun: done(core.VerdictPass, nil)},
	}
	got := historyPoints(results, runs)
	if len(got) != 2 || got[0].RunID != 1 || !got[0].RunStartedAt.Equal(t0) || got[0].Steps != 3 || got[0].Status != "failed" || got[1].Status != "passed" || got[1].RunID != 2 ||
		!got[1].RunStartedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("points: %+v", got)
	}
	if historyPoints(nil, runs) == nil {
		t.Fatal("empty history must be an empty list, not nil")
	}
}
