package agent_research_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
)

// writeDefs writes the files (name -> content) to a new directory and loads them as the agent definitions.
func writeDefs(t *testing.T, files map[string]string) *agent_defs.Registry {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
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
description = "Leads the research and starts scouts."
system_prompt = "LEAD PROMPT"
[spawn]
enabled = true
[[spawn.roles]]
name = "scout"
agent = "scout"
version = "^1.0"
when = "WHEN-TO-SCOUT the topic has a region of its own"
does = "DOES-SCOUTING one region"
max_steps = 3
max_steps_limit = 4
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
	return strings.Replace(call(tools...), `"usage":{"input_tokens":1,"output_tokens":1}`, `"usage":{"input_tokens":0,"output_tokens":0}`, 1)
}

func freeFinal(text string) string {
	return strings.Replace(final(text), `"usage":{"input_tokens":1,"output_tokens":1}`, `"usage":{"input_tokens":0,"output_tokens":0}`, 1)
}

// pricedClient prices the fake model "m" at 1 USD per token, so that a call of the fakes above (1 token in, 1 out) costs 2 USD.
func pricedClient(t *testing.T, f *teamFake) *ai.Client {
	return f.startWith(t, ai.WithPrices(ai.NewPriceTable(map[string]ai.Price{"m": {Input: 1e6, Output: 1e6}})))
}

func spawnRole(role, name, task string, maxSteps int) [2]string {
	in := `{"role":"` + role + `","name":"` + name + `","reason":"reason of ` + name + `","task":"` + task + `"`
	if maxSteps > 0 {
		in += `,"max_steps":` + string(rune('0'+maxSteps))
	}
	return [2]string{"spawn_subagent", in + `}`}
}

func TestRolesStartSubagentsOfTheirOwnDefinition(t *testing.T) { runOnBackends(t, testRoles) }

func testRoles(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	reg := writeDefs(t, map[string]string{"lead.toml": rootDef, "scout.toml": scoutDef})
	fake := newTeamFake(map[string][]string{
		"root": {
			call([2]string{"spawn_subagent", `{"name":"a","reason":"r","task":"t"}`}),            // no role
			call(spawnRole("scout", "asia", "cover asia", 9)),                                    // over the limit of the role
			call(spawnRole("nobody", "asia", "cover asia", 0)),                                   // not a role
			call(spawnRole("scout", "asia", "cover asia", 0), [2]string{"wait_subagents", `{}`}), // the role's default steps
			final("lead report"),
		},
		"asia": {final("asia report")},
	})
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	if err := m.UseAgentDefs(reg); err != nil {
		t.Fatal(err)
	}
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "the topic", Agent: "lead", Model: "root-model"})
	if err != nil {
		t.Fatal(err)
	}
	if started.AgentName != "lead" || started.AgentVersion != "1.4.0" || len(started.AgentHash) != 64 {
		t.Errorf("the run does not say which definition it runs: %+v", started)
	}
	m.Wait()

	root, _ := m.Get(ctx, "u1", started.ID)
	if root.Status != core.StatusCompleted || root.Report != "lead report" {
		t.Fatalf("root: %+v", root)
	}
	if root.Model != "root-model" || root.MaxSteps != agent_defs.DefaultMaxSteps {
		t.Errorf("root model and steps: %+v", root)
	}
	subs, _ := m.List(ctx, "u1", agent_research.ListOptions{ParentID: root.ID})
	if len(subs) != 1 {
		t.Fatalf("sub-agents: %+v", subs)
	}
	kid := subs[0]
	if kid.AgentName != "scout" || kid.AgentVersion != "1.2.0" || kid.AgentHash == "" || kid.AgentHash == root.AgentHash {
		t.Errorf("the sub-agent runs the definition of its role: %+v", kid)
	}
	// The sub-agent runs on the model of its own definition (the parent cannot choose), with the steps of the role.
	if kid.Model != "scout-model" || kid.MaxSteps != 3 || kid.Report != "asia report" || kid.Name != "asia" {
		t.Errorf("sub-agent: %+v", kid)
	}

	// The root is told its roles; the sub-agent has the prompt, the tools and the model of its definition, not the root's.
	first := fake.body("root", 0)
	for _, want := range []string{"LEAD PROMPT", "WHEN-TO-SCOUT", "DOES-SCOUTING", `"enum":["scout"]`} {
		if !strings.Contains(first, want) {
			t.Errorf("the root's first call lacks %q: %s", want, first)
		}
	}
	kidBody := fake.body("asia", 0)
	if !strings.Contains(kidBody, "SCOUT PROMPT") || strings.Contains(kidBody, "LEAD PROMPT") || !strings.Contains(kidBody, `"model":"scout-model"`) {
		t.Errorf("the sub-agent's call: %s", kidBody)
	}
	if strings.Contains(kidBody, `"name":"query"`) || strings.Contains(kidBody, `"name":"fetch_url"`) || !strings.Contains(kidBody, `"name":"web_search"`) {
		t.Errorf("the tools of the sub-agent are those of its definition: %s", kidBody)
	}
	// The three bad spawns were told to the model, and none started anything.
	for i, want := range map[int]string{1: `missing \"role\"`, 2: "max_steps of the role", 3: "unknown role"} {
		if b := fake.body("root", i); !strings.Contains(b, want) {
			t.Errorf("call %d of the root should have been told %q: %s", i, want, b)
		}
	}
}

func TestOnStartRolesAreStartedBeforeTheFirstCall(t *testing.T) { runOnBackends(t, testOnStart) }

func testOnStart(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	reg := writeDefs(t, map[string]string{
		"scout.toml": scoutDef,
		"lead.toml": `
name = "lead"
version = "1.0.0"
description = "Starts a news scout at once."
system_prompt = "LEAD PROMPT"
[spawn]
enabled = true
[[spawn.roles]]
name = "news"
agent = "scout"
trigger = "on_start"
does = "reads the news"
task_template = "News about {{.Topic}}"
`,
	})
	fake := newTeamFake(map[string][]string{
		"root": {call([2]string{"wait_subagents", `{}`}), final("lead report")},
		"news": {final("news report")},
	})
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	if err := m.UseAgentDefs(reg); err != nil {
		t.Fatal(err)
	}
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "the topic", Agent: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	root, _ := m.Get(ctx, "u1", started.ID)
	if root.Status != core.StatusCompleted || root.Report != "lead report" {
		t.Fatalf("root: %+v", root)
	}
	subs, _ := m.List(ctx, "u1", agent_research.ListOptions{ParentID: root.ID})
	if len(subs) != 1 || subs[0].Name != "news" || subs[0].Topic != "News about the topic" || subs[0].Reason != "reads the news" || subs[0].AgentName != "scout" || subs[0].Report != "news report" {
		t.Fatalf("the on_start sub-agent: %+v", subs)
	}
	first := fake.body("root", 0)
	if !strings.Contains(first, `Sub-agents \"news\" were started for you`) || !strings.Contains(first, `"name":"wait_subagents"`) || strings.Contains(first, `"name":"spawn_subagent"`) {
		t.Errorf("the root is told, has wait_subagents and no spawn_subagent: %s", first)
	}
	// The report of the sub-agent reached the root through wait_subagents.
	if b := fake.body("root", 1); !strings.Contains(b, "news report") {
		t.Errorf("the root did not get the report: %s", b)
	}
}

// The cap on the cost of the whole research stops its agents (the last call is made without tools), and the report says so.
func TestTreeCostCapStopsTheAgentsAndIsSaidInTheReport(t *testing.T) { runOnBackends(t, testTreeCap) }

func testTreeCap(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(map[string][]string{
		// The root is free; the sub-agent's calls cost 2 USD each. Spawning and waiting in one answer makes the order certain: when the
		// root makes its second call the sub-agent has ended and the research has spent 2 USD, over its cap of 1.
		"root": {freeCall(spawn("kid", "look"), [2]string{"wait_subagents", `{}`}), freeFinal("combined report")},
		"kid":  {call([2]string{"list_siblings", `{}`}), final("kid report")},
	})
	m := agent_research.NewManager(ctx, d, ops, pricedClient(t, fake))
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "the topic", MaxCostUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	root, _ := m.Get(ctx, "u1", started.ID)
	if root.Status != core.StatusCompleted {
		t.Fatalf("root: %+v", root)
	}
	if !strings.HasPrefix(root.Report, "combined report") || !strings.Contains(root.Report, "Stopped early: the cost cap of this research ($1.00)") {
		t.Errorf("the report of the root does not say that the cap stopped the research:\n%s", root.Report)
	}
	subs, _ := m.List(ctx, "u1", agent_research.ListOptions{ParentID: root.ID})
	if len(subs) != 1 || subs[0].Status != core.StatusCompleted || !strings.HasPrefix(subs[0].Report, "kid report") || !strings.Contains(subs[0].Report, "Stopped early: the cost cap of this research") {
		t.Fatalf("the sub-agent was cut short and says so: %+v", subs)
	}
	if subs[0].Steps != 2 {
		t.Errorf("the sub-agent made %d calls: the second was the forced last one", subs[0].Steps)
	}
	// The last call was made without tools and the agent was told why.
	if b := fake.body("kid", 1); !strings.Contains(b, `"tool_choice":{"type":"none"}`) || !strings.Contains(b, "[Budget reached] The cost cap of this research ($1.00") {
		t.Errorf("the sub-agent's last call: %s", b)
	}
	// What the root got from wait_subagents is the report with the note, and its own last call was told too.
	if b := fake.body("root", 1); !strings.Contains(b, "Stopped early") || !strings.Contains(b, "[Budget reached]") {
		t.Errorf("the root's last call: %s", b)
	}
	// The state of the last call says it was early.
	var last core.State
	for _, s := range root.States {
		if s.State == core.StateWritingReport {
			last = s
		}
	}
	if !strings.Contains(last.Detail, "made early") {
		t.Errorf("writing_report: %+v", last)
	}
}

func TestAgentCostCapAndNoCapNoNote(t *testing.T) { runOnBackends(t, testAgentCap) }

func testAgentCap(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	reg := writeDefs(t, map[string]string{"frugal.toml": `
name = "frugal"
version = "1.0.0"
description = "Cheap."
system_prompt = "FRUGAL"
[limits]
max_cost_usd = 1
`})
	fake := newTeamFake(map[string][]string{"root": {call([2]string{"wait_subagents", `{}`}), final("partial report")}})
	m := agent_research.NewManager(ctx, d, ops, pricedClient(t, fake))
	if err := m.UseAgentDefs(reg); err != nil {
		t.Fatal(err)
	}
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "the topic", Agent: "frugal"})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	run, _ := m.Get(ctx, "u1", started.ID)
	if run.Status != core.StatusCompleted || !strings.HasPrefix(run.Report, "partial report") || !strings.Contains(run.Report, "Stopped early: this agent's cost cap ($1.00)") || run.Steps != 2 {
		t.Errorf("run: %+v", run)
	}

	// With the cap out of reach nothing is said and nothing is cut.
	fake = newTeamFake(map[string][]string{"root": {call([2]string{"wait_subagents", `{}`}), final("full report")}})
	m = agent_research.NewManager(ctx, d, ops, pricedClient(t, fake))
	started, err = m.Start(ctx, "u1", agent_research.Options{Topic: "the topic", MaxCostUSD: 1000})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	run, _ = m.Get(ctx, "u1", started.ID)
	if run.Report != "full report" {
		t.Errorf("report: %q", run.Report)
	}
}

func TestServerCostCeilingLowersTheCaps(t *testing.T) { runOnBackends(t, testCeiling) }

func testCeiling(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	fake := newTeamFake(map[string][]string{"root": {call([2]string{"wait_subagents", `{}`}), final("report")}})
	m := agent_research.NewManager(ctx, d, ops, pricedClient(t, fake))
	m.MaxCostUSD = 1
	// The request asks for more than the ceiling: the ceiling wins.
	started, err := m.Start(ctx, "u1", agent_research.Options{Topic: "the topic", MaxCostUSD: 100})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	run, _ := m.Get(ctx, "u1", started.ID)
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
	runOnBackends(t, func(t *testing.T, d *sql.DB, ops agent_research.Ops) {
		m := agent_research.NewManager(ctx, d, ops, newTeamFake(nil).start(t))
		if err := m.UseAgentDefs(reg); err != nil {
			t.Fatal(err)
		}
		for name, opts := range map[string]agent_research.Options{
			"unknown agent":     {Topic: "t", Agent: "nobody"},
			"bad agent name":    {Topic: "t", Agent: "No Body"},
			"version not met":   {Topic: "t", Agent: "scout", AgentVersion: ">=2.0"},
			"bad version":       {Topic: "t", Agent: "scout", AgentVersion: "x.y"},
			"locked dataset":    {Topic: "t", Agent: "pinned", Dataset: "other"},
			"negative cost cap": {Topic: "t", MaxCostUSD: -1},
			"too many steps":    {Topic: "t", Agent: "scout", MaxSteps: 41},
		} {
			if _, err := m.Start(ctx, "u1", opts); err == nil {
				t.Errorf("%s: should have been refused", name)
			} else if e, ok := err.(*agent_research.Error); !ok || e.Kind != agent_research.KindInvalid {
				t.Errorf("%s: %v", name, err)
			}
		}
		infos := m.Agents()
		var names []string
		for _, i := range infos {
			names = append(names, i.Name+"@"+i.Version)
		}
		if got := strings.Join(names, ","); got != "default@1.0.0,pinned@1.0.0,scout@1.2.0" {
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

func TestScheduledAgentsRunWhenDue(t *testing.T) { runOnBackends(t, testScheduled) }

func testScheduled(t *testing.T, d *sql.DB, ops agent_research.Ops) {
	ctx := context.Background()
	reg := writeDefs(t, map[string]string{
		"daily.toml":  scheduledDef,
		"paused.toml": strings.NewReplacer(`"daily"`, `"paused"`, `[schedule]`, "[schedule]\npaused = true").Replace(scheduledDef),
		"lead.toml":   rootDef, "scout.toml": scoutDef, // no schedule
	})
	fake := newTeamFake(map[string][]string{"root": {final("morning report"), final("second report")}})
	m := agent_research.NewManager(ctx, d, ops, fake.start(t))
	if err := m.UseAgentDefs(reg); err != nil {
		t.Fatal(err)
	}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	at := func(day, h, min int) time.Time { return time.Date(2030, 1, day, h, min, 0, 0, berlin) }
	tick := agent_research.ScheduleTicker(m, at(1, 7, 0)) // first due: 1 January 7:30

	tick(at(1, 7, 29))
	m.Wait()
	if runs, _ := m.List(ctx, "cron_user", agent_research.ListOptions{}); len(runs) != 0 {
		t.Fatalf("started before it was due: %+v", runs)
	}
	tick(at(1, 7, 31))
	m.Wait()
	tick(at(1, 12, 0)) // not due again until the next morning
	m.Wait()
	runs, _ := m.List(ctx, "cron_user", agent_research.ListOptions{})
	if len(runs) != 1 {
		t.Fatalf("runs: %+v", runs)
	}
	if r := runs[0]; r.AgentName != "daily" || r.Topic != "News of 2030-01-01" || r.Status != core.StatusCompleted || r.Report != "morning report" {
		t.Errorf("the scheduled run: %+v", r)
	}
	// The server was down for three days: the next run comes once, not once per missed day.
	tick(at(4, 9, 0))
	m.Wait()
	runs, _ = m.List(ctx, "cron_user", agent_research.ListOptions{})
	if len(runs) != 2 {
		t.Fatalf("runs after the downtime: %d", len(runs))
	}
	if all, _ := m.List(ctx, "scheduler", agent_research.ListOptions{}); len(all) != 0 {
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
	runOnBackends(t, func(t *testing.T, d *sql.DB, ops agent_research.Ops) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reg := writeDefs(t, map[string]string{"daily.toml": strings.Replace(scheduledDef, "30 7 * * *", "*/10 * * * *", 1)})
		fake := newTeamFake(map[string][]string{"root": {final("report")}})
		fake.hang = map[string]bool{"root": true} // the first run never answers
		m := agent_research.NewManager(ctx, d, ops, fake.start(t))
		if err := m.UseAgentDefs(reg); err != nil {
			t.Fatal(err)
		}
		start := time.Date(2030, 1, 1, 6, 0, 0, 0, time.UTC)
		tick := agent_research.ScheduleTicker(m, start)
		tick(start.Add(11 * time.Minute)) // starts and waits for the model
		tick(start.Add(21 * time.Minute)) // the first still works: skipped
		if runs, _ := m.List(ctx, "cron_user", agent_research.ListOptions{}); len(runs) != 1 {
			t.Fatalf("runs while the first works: %d", len(runs))
		}
		cancel()
		m.Wait()
	})
}
