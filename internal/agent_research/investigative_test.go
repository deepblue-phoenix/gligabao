package agent_research_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const investigativeDef = `
name = "deep"
version = "1.0.0"
description = "Investigates."
system_prompt = "DEEP PROMPT"
loop = "investigative"
[investigate]
max_iterations = 12
min_sources = 1
min_domains = 1
`

// reply builds a fake Messages API answer that calls the tools (name, input) in order.
func toolReply(calls ...[2]any) string {
	var content []any
	for i, c := range calls {
		content = append(content, map[string]any{"type": "tool_use", "id": "tu_" + string(rune('a'+i)), "name": c[0], "input": c[1]})
	}
	b, _ := json.Marshal(map[string]any{"model": "m", "stop_reason": "tool_use", "content": content, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}})
	return string(b)
}

type kv = map[string]any

// The investigative loop through the manager: the run is recorded with its states, phase and stop reason, the notes file is written, and the
// report has its sources as links.
func TestInvestigativeRunThroughTheManager(t *testing.T) { runOnBackends(t, testInvestigativeRun) }

func testInvestigativeRun(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := &fakeAnthropic{main: []string{
		toolReply([2]any{"add_question", kv{"parent_id": "q_001", "question": "Why are batteries good?", "priority": 0.8, "validation_criteria": "a reason"}}),
		toolReply(
			[2]any{"notes_append", kv{"section": "Sources", "entries": []kv{{"url": "https://a.example/x", "title": "A"}}}},
			[2]any{"resolve_question", kv{"id": "q_002", "answer": "they store energy", "reason": "shallow"}},
			[2]any{"resolve_question", kv{"id": "q_001", "answer": "batteries are good", "reason": "shallow"}},
			[2]any{"notes_scratch", kv{"text": "Conclusion: batteries are good."}},
			[2]any{"challenge", kv{"target_type": "conclusion", "negation": "batteries are not good", "strategy": "negation", "expected_if_false": "e", "query": "batteries not good official"}},
			[2]any{"web_search", kv{"query": "batteries not good official"}},
			[2]any{"record_challenge_result", kv{"challenge_id": "ch_001", "outcome": "survived"}},
		),
		toolReply([2]any{"finalize_report", kv{"report": "# Report\nBatteries are good (s_001)."}}),
	}}
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	m.NotesDir = filepath.Join(t.TempDir(), "notes")
	if err := m.UseAgentDefs(writeDefs(t, map[string]string{"deep.toml": investigativeDef})); err != nil {
		t.Fatal(err)
	}
	started, err := m.Start(ctx, "u/1", agent_research.Options{Topic: "batteries", Agent: "deep"})
	if err != nil {
		t.Fatal(err)
	}
	if started.MaxSteps != 12 {
		t.Errorf("max_iterations is the steps of the run: %d", started.MaxSteps)
	}
	m.Wait()
	run, err := m.Get(ctx, "u/1", started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != core.StatusCompleted || run.Error != "" || run.Phase != "synthesis" || run.StopReason != "frontier_exhausted" || run.Steps != 3 {
		t.Fatalf("run: %+v", run)
	}
	if !strings.Contains(run.Report, "[s_001](https://a.example/x)") {
		t.Errorf("report: %s", run.Report)
	}
	states := stateNames(run.States)
	for _, want := range []string{"planning", "selecting", "synthesizing", "completed"} {
		if !strings.Contains(states, want) {
			t.Errorf("states %s lack %s", states, want)
		}
	}
	// The planning request offers the investigative tools, with fetch_url named fetch_page; the synthesis one has no search tool.
	if !strings.Contains(fake.bodies[0], `"name":"add_question"`) || !strings.Contains(fake.bodies[0], `"name":"fetch_page"`) || strings.Contains(fake.bodies[0], `"name":"fetch_url"`) {
		t.Error("tools of the planning turn")
	}
	last := fake.bodies[len(fake.bodies)-1]
	if strings.Contains(last, `"name":"web_search"`) || !strings.Contains(last, `"name":"finalize_report"`) {
		t.Error("tools of the synthesis turn")
	}
	runDocQueries(t, d, run.ID, strings.HasSuffix(t.Name(), "postgres"))
	entries, err := os.ReadDir(m.NotesDir)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "notes_u_1_") || !strings.Contains(entries[0].Name(), run.NotesID) {
		t.Fatalf("notes file: %v %v", entries, err)
	}
}

// Run options and the definition's knobs are checked.
func TestInvestigativeOptions(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenSQLite(t)
	fake := &fakeAnthropic{}
	m := agent_research.NewManager(ctx, d, agent_research.SQLiteOps, fake.start(t))
	if err := m.UseAgentDefs(writeDefs(t, map[string]string{"deep.toml": investigativeDef})); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		opts agent_research.Options
		want string
	}{
		"knobs with the simple loop": {agent_research.Options{Topic: "t", Investigate: &agent_defs.Investigate{MaxIterations: 5}}, "only take effect with the investigative loop"},
		"max_steps":                  {agent_research.Options{Topic: "t", Agent: "deep", MaxSteps: 5}, "max_steps does not apply"},
		"bad knob":                   {agent_research.Options{Topic: "t", Agent: "deep", Investigate: &agent_defs.Investigate{SynthesisReserve: 0.9}}, "synthesis_reserve"},
		"modal":                      {agent_research.Options{Topic: "t", Agent: "deep", Runner: "modal"}, "runs locally"},
	} {
		_, err := m.Start(ctx, "u", c.opts)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// loop = "simple" is the default and its first request does not change: the golden file is the body of the first model call of the
// built-in agent (run with -update to rewrite it after a deliberate change of the prompt).
func TestSimpleLoopFirstRequestGolden(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenSQLite(t)
	fake := &fakeAnthropic{main: []string{finalReport}}
	m := agent_research.NewManager(ctx, d, agent_research.SQLiteOps, fake.start(t))
	if _, err := m.Start(ctx, "u1", agent_research.Options{Topic: "solid state batteries", MaxSteps: 3, Model: "my-model"}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	var pretty map[string]any
	if err := json.Unmarshal([]byte(fake.bodies[0]), &pretty); err != nil {
		t.Fatal(err)
	}
	got, _ := json.MarshalIndent(pretty, "", "  ")
	path := filepath.Join("testdata", "simple_first_request.golden.json")
	if *update {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(want)) != string(got) {
		t.Errorf("the first request of the simple loop changed:\n%s", got)
	}
	run, _ := m.Get(ctx, "u1", 1)
	if run.Phase != "" || run.StopReason != "" || run.Status != core.StatusCompleted {
		t.Errorf("a simple run has no phase: %+v", run)
	}
}

// --- migrations of phase and stop_reason ---

// previousDatabase is a database made by the previous version (0.180.0): gb_agent_research_runs with notes_id and without phase and
// stop_reason.
func previousDatabase(t *testing.T, backend string) *sql.DB {
	t.Helper()
	d := oldDatabase(t, backend)
	if _, err := d.Exec(`ALTER TABLE gb_agent_research_runs ADD COLUMN notes_id TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestMigrationAddsPhaseAndStopReason(t *testing.T) {
	for _, backend := range []string{config.DBBackendSQLite, config.DBBackendPostgres} {
		t.Run(backend, func(t *testing.T) {
			d := previousDatabase(t, backend)
			for _, c := range []string{"phase", "stop_reason"} {
				if hasColumn(t, d, backend, "gb_agent_research_runs", c) {
					t.Fatalf("the previous database must not have %s", c)
				}
			}
			check := func() {
				t.Helper()
				for _, c := range []string{"phase", "stop_reason"} {
					if !hasColumn(t, d, backend, "gb_agent_research_runs", c) {
						t.Fatalf("%s was not added", c)
					}
				}
				var phase, stop, topic string
				if err := d.QueryRow(`SELECT phase, stop_reason, topic FROM gb_agent_research_runs`).Scan(&phase, &stop, &topic); err != nil || phase != "" || stop != "" || topic != "old topic" {
					t.Fatalf("old row: %q %q %q %v", phase, stop, topic, err)
				}
			}
			if err := agent_research.Open(backend, d); err != nil {
				t.Fatal(err)
			}
			check()
			if err := agent_research.Open(backend, d); err != nil { // a second start changes nothing
				t.Fatalf("second start: %v", err)
			}
			check()

			// The phase and the stop reason are written and read back on the upgraded database.
			ops := agent_research.SQLiteOps
			if backend == config.DBBackendPostgres {
				ops = agent_research.PostgresOps
			}
			ctx := context.Background()
			run := &core.Run{UserID: "u", Topic: "t", State: core.StateStarting}
			if err := ops.Store.CreateRun(ctx, d, run); err != nil {
				t.Fatal(err)
			}
			if err := ops.Store.SetPhase(ctx, d, run.ID, "synthesis", "breaker:cost"); err != nil {
				t.Fatal(err)
			}
			got, err := ops.Store.GetRun(ctx, d, run.ID)
			if err != nil || got.Phase != "synthesis" || got.StopReason != "breaker:cost" {
				t.Fatalf("read back: %+v %v", got, err)
			}
			if err := ops.Store.SetPhase(ctx, d, 99999, "x", "y"); err == nil {
				t.Error("an unknown run")
			}

			// The simple loop still runs on the upgraded database.
			fake := &fakeAnthropic{main: []string{finalReport}}
			m := agent_research.NewManager(ctx, d, ops, fake.start(t))
			started, err := m.Start(ctx, "u2", agent_research.Options{Topic: "batteries", MaxSteps: 2})
			if err != nil {
				t.Fatal(err)
			}
			m.Wait()
			r, err := m.Get(ctx, "u2", started.ID)
			if err != nil || r.Status != core.StatusCompleted || r.Phase != "" || r.StopReason != "" {
				t.Fatalf("simple run: %+v %v", r, err)
			}
		})
	}
}

// A fresh database has the columns from schema.sql, and the upgrade is a no-op.
func TestFreshDatabaseHasPhaseAndStopReason(t *testing.T) {
	dbtest.Run(t, func(t *testing.T, d *sql.DB) {
		backend := config.DBBackendSQLite
		if strings.HasSuffix(t.Name(), "postgres") {
			backend = config.DBBackendPostgres
		}
		for _, c := range []string{"phase", "stop_reason"} {
			if !hasColumn(t, d, backend, "gb_agent_research_runs", c) {
				t.Errorf("a fresh database lacks %s", c)
			}
		}
		if err := agent_research.Open(backend, d); err != nil {
			t.Fatalf("the upgrade is a no-op: %v", err)
		}
	})
}

var docQuery = regexp.MustCompile("(?s)<!-- query: (\\w+) -->\n```sql\n(.*?)```")

// runDocQueries runs every example query of docs/agents/research.md ("Reading a run with SQL") on the run and checks that it works and, for
// the ones that must return rows here, that it does.
func runDocQueries(t *testing.T, d *sql.DB, runID int64, postgres bool) {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "agents", "research.md"))
	if err != nil {
		t.Fatal(err)
	}
	found := docQuery.FindAllStringSubmatch(string(doc), -1)
	if len(found) < 7 {
		t.Fatalf("found %d queries in the doc", len(found))
	}
	mustReturn := map[string]bool{"tree": true, "transitions": true, "challenges_per_target_and_strategy": true, "outcome_distribution": true, "notes_at_each_write": true, "runs_by_stop_reason": true}
	for _, f := range found {
		name, query := f[1], f[2]
		args := []any{runID}
		if postgres {
			query = strings.ReplaceAll(query, "?1", "$1")
		}
		if strings.Contains(query, "FROM gb_agent_research_runs WHERE phase") {
			args = nil
		}
		rows, err := d.Query(query, args...)
		if err != nil {
			t.Errorf("query %s: %v", name, err)
			continue
		}
		n := 0
		for rows.Next() {
			n++
		}
		if err := rows.Err(); err != nil {
			t.Errorf("query %s: %v", name, err)
		}
		rows.Close()
		if mustReturn[name] && n == 0 {
			t.Errorf("query %s returned no rows", name)
		}
	}
}
