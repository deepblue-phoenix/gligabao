package agent_research_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
)

func TestNoTrajectoriesByDefault(t *testing.T) {
	runOnBackends(t, func(t *testing.T, d *sql.DB, ops agent_research.Ops) {
		ctx := context.Background()
		fake := newTeamFake(map[string][]string{"root": {final("report")}})
		m := agent_research.NewManager(ctx, d, ops, fake.start(t))
		if _, err := m.Start(ctx, "u1", agent_research.Options{Topic: "the topic", MaxSteps: 5}); err != nil {
			t.Fatal(err)
		}
		m.Wait() // nothing to receive; it must not panic
	})
}
