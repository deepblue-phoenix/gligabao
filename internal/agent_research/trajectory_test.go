package agent_research_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
)

func TestSubagentTrajectoriesAreMergedIntoTheRoots(t *testing.T) {
	runOnBackends(t, testMergedTrajectories)
}

func testMergedTrajectories(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(map[string][]string{
		"root": {
			call(spawn("alpha", "find the history")),
			call([2]string{"wait_subagents", `{}`}),
			final("combined report"),
		},
		"alpha": {call(spawn("deep", "dig"), [2]string{"read_messages", `{}`}), call([2]string{"wait_subagents", `{}`}), final("alpha report")},
		"deep":  {final("deep report")},
	})
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	var mu sync.Mutex
	got := map[int64]*atif.Trajectory{}
	m.UseTrajectories(func(id int64, tr *atif.Trajectory) {
		mu.Lock()
		got[id] = tr
		mu.Unlock()
	})

	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "the topic", MaxSteps: 5})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()

	// One trajectory, the root's, for the whole research.
	if len(got) != 1 || got[started.ID] == nil {
		t.Fatalf("trajectories: %v", got)
	}
	tr := got[started.ID]
	if tr.SessionID != agent_research.SessionID(started.ID) {
		t.Errorf("session: %q", tr.SessionID)
	}
	perAgent := map[string]int{}
	var tags []string
	for i, s := range tr.Steps {
		if s.StepID != i+1 {
			t.Fatalf("step %d has id %d", i+1, s.StepID)
		}
		name, _ := s.Extra["agent"].(string)
		perAgent[name]++
		if s.Source == atif.SourceUser {
			tags = append(tags, name)
		}
	}
	// system+user+3 model calls for the root; system+user+3 for alpha; system+user+1 for deep.
	if perAgent["main"] != 5 || perAgent["alpha"] != 5 || perAgent["deep"] != 3 {
		t.Fatalf("steps per agent: %v", perAgent)
	}
	if len(tags) != 3 {
		t.Fatalf("user steps: %v", tags)
	}
	// The sub-agents' steps carry where they are in the tree.
	for _, s := range tr.Steps {
		switch s.Extra["agent"] {
		case "alpha":
			if s.Extra["depth"] != 1 || s.Extra["parent_run_id"] != started.ID {
				t.Fatalf("alpha step: %+v", s.Extra)
			}
		case "deep":
			if s.Extra["depth"] != 2 || s.Extra["parent_run_id"] == started.ID {
				t.Fatalf("deep step: %+v", s.Extra)
			}
		}
	}
	// The final metrics are those of all the model calls: 3 + 3 + 1, one token in and out each.
	if fm := tr.FinalMetrics; fm.TotalPromptTokens != 7 || fm.TotalCompletionTokens != 7 || fm.TotalSteps != len(tr.Steps) {
		t.Fatalf("final metrics: %+v", fm)
	}
	// The "agents" extra, as it is in the JSON.
	data, err := atif.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Extra struct {
			Agents []struct {
				Agent  string `json:"agent"`
				Depth  int    `json:"depth"`
				Status string `json:"status"`
				Steps  int    `json:"steps"`
			} `json:"agents"`
		} `json:"extra"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	agents := doc.Extra.Agents
	if len(agents) != 3 || agents[0].Agent != "main" || agents[1].Agent != "alpha" || agents[2].Agent != "deep" {
		t.Fatalf("agents: %+v", agents)
	}
	if agents[0].Status != "completed" || agents[0].Steps != 3 || agents[2].Depth != 2 {
		t.Fatalf("agents: %+v", agents)
	}
}

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
