package agent_research_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
)

// writeDefs writes the files (name -> content) to a new directory and loads them as the agent definitions.
func writeDefs(t *testing.T,
	files map[string]string) *agent_defs.Registry {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path,
			[]byte(content),
			0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := agent_defs.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

const (
	rootDef = `
name = "lead"
version = "1.4.0"
description = "Leads the research."
system_prompt = "LEAD PROMPT"
`
	scoutDef = `
name = "scout"
version = "1.2.0"
description = "Scouts a region."
system_prompt = "SCOUT PROMPT"
model = "scout-model"
max_steps = 6
tools = ["web_search"]
[knowledge]
access = "none"
`
)

// A free call and a call that costs 2 USD at the price table of pricedClient.
func freeCall(tools ...[2]string) string {
	return strings.Replace(call(tools...),
		`"usage":{"input_tokens":1,"output_tokens":1}`,
		`"usage":{"input_tokens":0,"output_tokens":0}`,
		1)
}

func freeFinal(text string) string {
	return strings.Replace(final(text),
		`"usage":{"input_tokens":1,"output_tokens":1}`,
		`"usage":{"input_tokens":0,"output_tokens":0}`,
		1)
}

// pricedClient prices the fake model "m" at 1 USD per token, so that a call of the fakes above (1 token in, 1 out) costs 2 USD.
func pricedClient(t *testing.T,
	f *teamFake) *ai.Client {
	return f.startWith(t, ai.WithPrices(ai.NewPriceTable(map[string]ai.Price{"m": {
		Input:  1e6,
		Output: 1e6,
	}})))
}

// The cap on the cost of the whole research counts what the workers spend: the supervisor's own calls are free here, the two workers cost 2 USD
// each, so the cap of 4.4 (3.74 of it for the exploration) is passed by them, the loop goes to its synthesis (stop reason breaker:cost) and the report is written.
func TestTreeCostCapCountsTheWorkers(t *testing.T) {
	runOnBackends(t, testTreeCap)
}

func testTreeCap(t *testing.T,
	d *sql.DB,
	ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(map[string][]string{
		"root": {
			freeCall(
				[2]string{"add_question", `{"parent_id":"q_001","question":"Why are batteries good?","priority":0.8,"validation_criteria":"a reason"}`}, [2]string{"add_question", `{"parent_id":"q_001","question":"What do batteries cost?","priority":0.7,"validation_criteria":"a price"}`}),
			freeCall([2]string{"finalize_report", `{"report":"# Report\nBatteries (s_001) are good.\n\n## Gaps and assumptions\n- q_002 and q_003 are not finished: the budget ran out."}`}),
		},
		"q_002": {answered("https://a.example/x")},
		"q_003": {answered("https://b.example/y")},
	})
	m := agent_research.NewManager(ctx,
		d,
		ops,
		pricedClient(t, fake))
	m.NotesDir = filepath.Join(t.TempDir(), "notes")
	if err := m.UseAgentDefs(writeDefs(t, map[string]string{"deep.toml": workersDef})); err != nil {
		t.Fatal(err)
	}
	started, err := m.Start(ctx,
		"u1",
		agent_research.Options{
			Topic:      "batteries",
			Agent:      "deep",
			MaxCostUSD: 4.4,
		})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	root, _ := m.Get(ctx,
		"u1",
		started.ID)
	if root.Status != core.StatusCompleted || root.StopReason != "breaker:cost" || root.Phase != "synthesis" {
		t.Fatalf("root: %+v", root)
	}
	if root.CostUSD != 0 {
		t.Errorf("the cost of the workers is not the supervisor's own: %v", root.CostUSD)
	}
	subs, _ := m.List(ctx,
		"u1",
		agent_research.ListOptions{
			ParentID: root.ID,
		})
	if len(subs) != 2 || subs[0].CostUSD != 2 || subs[1].CostUSD != 2 {
		t.Fatalf("workers: %+v", subs)
	}
}

func TestAgentCostCapAndNoCapNoNote(t *testing.T) {
	runOnBackends(t, testAgentCap)
}

func testAgentCap(t *testing.T,
	d *sql.DB,
	ops agent_research.Ops) {
	ctx := context.Background()
	reg := writeDefs(t, map[string]string{"frugal.toml": `
name = "frugal"
version = "1.0.0"
description = "Cheap."
system_prompt = "FRUGAL"
[limits]
max_cost_usd = 1
`})
	fake := newTeamFake(map[string][]string{"root": {call([2]string{"no_such_tool", `{}`}), final("partial report")}})
	m := agent_research.NewManager(ctx,
		d,
		ops,
		pricedClient(t, fake))
	if err := m.UseAgentDefs(reg); err != nil {
		t.Fatal(err)
	}
	started, err := m.Start(ctx,
		"u1",
		agent_research.Options{
			Topic: "the topic",
			Agent: "frugal",
		})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	run, _ := m.Get(ctx,
		"u1",
		started.ID)
	if run.Status != core.StatusCompleted || !strings.HasPrefix(run.Report, "partial report") || !strings.Contains(run.Report, "Stopped early: this agent's cost cap ($1.00)") || run.Steps != 2 {
		t.Errorf("run: %+v", run)
	}

	// With the cap out of reach nothing is said and nothing is cut.
	fake = newTeamFake(map[string][]string{"root": {call([2]string{"no_such_tool", `{}`}), final("full report")}})
	m = agent_research.NewManager(ctx,
		d,
		ops,
		pricedClient(t, fake))
	started, err = m.Start(ctx,
		"u1",
		agent_research.Options{
			Topic:      "the topic",
			MaxCostUSD: 1000,
		})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	run, _ = m.Get(ctx,
		"u1",
		started.ID)
	if run.Report != "full report" {
		t.Errorf("report: %q", run.Report)
	}
}

func TestServerCostCeilingLowersTheCaps(t *testing.T) {
	runOnBackends(t, testCeiling)
}

func testCeiling(t *testing.T,
	d *sql.DB,
	ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(map[string][]string{"root": {call([2]string{"no_such_tool", `{}`}), final("report")}})
	m := agent_research.NewManager(ctx,
		d,
		ops,
		pricedClient(t, fake))
	m.MaxCostUSD = 1
	// The request asks for more than the ceiling: the ceiling wins.
	started, err := m.Start(ctx,
		"u1",
		agent_research.Options{
			Topic:      "the topic",
			MaxCostUSD: 100,
		})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	run, _ := m.Get(ctx,
		"u1",
		started.ID)
	if !strings.Contains(run.Report, "Stopped early: this agent's cost cap") && !strings.Contains(run.Report, "Stopped early: the cost cap of this research ($1.00)") {
		t.Errorf("the ceiling did not apply: %q", run.Report)
	}
}

func TestStartResolvesTheAgentDefinition(t *testing.T) {
	ctx := context.Background()
	reg := writeDefs(t, map[string]string{
		"scout.toml": scoutDef,
		"pinned.toml": `
name = "pinned"
version = "1.0.0"
description = "Works in one dataset."
system_prompt = "PINNED"
[knowledge]
dataset = "reports"
dataset_locked = true
`,
	})
	runOnBackends(t, func(t *testing.T,
		d *sql.DB,
		ops agent_research.Ops) {
		m := agent_research.NewManager(ctx,
			d,
			ops,
			newTeamFake(nil).start(t))
		if err := m.UseAgentDefs(reg); err != nil {
			t.Fatal(err)
		}
		for name, opts := range map[string]agent_research.Options{
			"unknown agent": {
				Topic: "t",
				Agent: "nobody",
			},
			"bad agent name": {
				Topic: "t",
				Agent: "No Body",
			},
			"version not met": {
				Topic:        "t",
				Agent:        "scout",
				AgentVersion: ">=2.0",
			},
			"bad version": {
				Topic:        "t",
				Agent:        "scout",
				AgentVersion: "x.y",
			},
			"locked dataset": {
				Topic:   "t",
				Agent:   "pinned",
				Dataset: "other",
			},
			"negative cost cap": {
				Topic:      "t",
				MaxCostUSD: -1,
			},
			"too many steps": {
				Topic:    "t",
				Agent:    "scout",
				MaxSteps: 41,
			},
		} {
			if _, err := m.Start(ctx,
				"u1",
				opts); err == nil {
				t.Errorf("%s: should have been refused", name)
			} else if e, ok := err.(*agent_research.Error); !ok || e.Kind != agent_research.KindInvalid {
				t.Errorf("%s: %v",
					name,
					err)
			}
		}
		infos := m.Agents()
		var names []string
		for _, i := range infos {
			names = append(names, i.Name+"@"+i.Version)
		}
		if got := strings.Join(names, ","); got != "default@1.0.0,investigator@1.0.0,pinned@1.0.0,scout@1.2.0" {
			t.Errorf("agents: %s", got)
		}
	})
}

const scheduledDef = `
name = "daily"
version = "1.0.0"
description = "Runs every morning."
system_prompt = "DAILY PROMPT"
[schedule]
cron = "30 7 * * *"
timezone = "Europe/Berlin"
topic = "News of {{.Date}}"
user = "cron_user"
`

func TestScheduledAgentsRunWhenDue(t *testing.T) {
	runOnBackends(t, testScheduled)
}

func testScheduled(t *testing.T,
	d *sql.DB,
	ops agent_research.Ops) {
	ctx := context.Background()
	reg := writeDefs(t, map[string]string{
		"daily.toml": scheduledDef,
		"paused.toml": strings.NewReplacer(`"daily"`,
			`"paused"`,
			`[schedule]`,
			"[schedule]\npaused = true").Replace(scheduledDef),
		"lead.toml": rootDef, "scout.toml": scoutDef, // no schedule
	})
	fake := newTeamFake(map[string][]string{"root": {final("morning report"), final("second report")}})
	m := agent_research.NewManager(ctx,
		d,
		ops,
		fake.start(t))
	if err := m.UseAgentDefs(reg); err != nil {
		t.Fatal(err)
	}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	at := func(day,
		h,
		min int) time.Time {
		return time.Date(2030,
			1,
			day,
			h,
			min,
			0,
			0,
			berlin)
	}
	tick := agent_research.ScheduleTicker(m, at(1,
		7,
		0)) // first due: 1 January 7:30

	tick(at(1,
		7,
		29))
	m.Wait()
	if runs, _ := m.List(ctx,
		"cron_user",
		agent_research.ListOptions{}); len(runs) != 0 {
		t.Fatalf("started before it was due: %+v", runs)
	}
	tick(at(1,
		7,
		31))
	m.Wait()
	tick(at(1,
		12,
		0)) // not due again until the next morning
	m.Wait()
	runs, _ := m.List(ctx,
		"cron_user",
		agent_research.ListOptions{})
	if len(runs) != 1 {
		t.Fatalf("runs: %+v", runs)
	}
	if r := runs[0]; r.AgentName != "daily" || r.Topic != "News of 2030-01-01" || r.Status != core.StatusCompleted || r.Report != "morning report" {
		t.Errorf("the scheduled run: %+v", r)
	}
	// The server was down for three days: the next run comes once, not once per missed day.
	tick(at(4,
		9,
		0))
	m.Wait()
	runs, _ = m.List(ctx,
		"cron_user",
		agent_research.ListOptions{})
	if len(runs) != 2 {
		t.Fatalf("runs after the downtime: %d", len(runs))
	}
	if all, _ := m.List(ctx,
		"scheduler",
		agent_research.ListOptions{}); len(all) != 0 {
		t.Errorf("the paused agent ran: %+v", all)
	}

	var info *agent_research.ScheduleInfo
	for _, a := range m.Agents() {
		if a.Name == "daily" {
			info = a.Schedule
		}
		if a.Name == "lead" && a.Schedule != nil {
			t.Errorf("lead has no schedule: %+v", a.Schedule)
		}
	}
	if info == nil || info.Cron != "30 7 * * *" || info.Timezone != "Europe/Berlin" || info.User != "cron_user" || info.Paused || info.NextRun == nil {
		t.Errorf("schedule in the list of agents: %+v", info)
	}
}

func TestScheduleSkipsARunWhileThePreviousOneWorks(t *testing.T) {
	runOnBackends(t, func(t *testing.T,
		d *sql.DB,
		ops agent_research.Ops) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reg := writeDefs(t, map[string]string{"daily.toml": strings.Replace(scheduledDef,
			"30 7 * * *",
			"*/10 * * * *",
			1)})
		fake := newTeamFake(map[string][]string{"root": {final("report")}})
		fake.hang = map[string]bool{"root": true} // the first run never answers
		m := agent_research.NewManager(ctx,
			d,
			ops,
			fake.start(t))
		if err := m.UseAgentDefs(reg); err != nil {
			t.Fatal(err)
		}
		start := time.Date(2030,
			1,
			1,
			6,
			0,
			0,
			0,
			time.UTC)
		tick := agent_research.ScheduleTicker(m, start)
		tick(start.Add(11 * time.Minute)) // starts and waits for the model
		tick(start.Add(21 * time.Minute)) // the first still works: skipped
		if runs, _ := m.List(ctx,
			"cron_user",
			agent_research.ListOptions{}); len(runs) != 1 {
			t.Fatalf("runs while the first works: %d", len(runs))
		}
		cancel()
		m.Wait()
	})
}
