package agent_research

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/agent/prompts"
	"github.com/deepblue-phoenix/gligabao/internal/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	log "github.com/sirupsen/logrus"
)

// researcher is the master struct of one research agent: everything the agent is and everything that changes while it works is in
// here, and nothing of it is shared with another agent. The Manager makes one for each Start and runs it on a goroutine of its own
// (work); only that goroutine touches it after that, so it needs no lock.
//
// What the agents do share is what is meant to be shared: the database handle and the store functions (the shared clients, safe
// for concurrent use) and the LLM client. The agent loop itself (agent.Agent, its conversation and totals) and the HTTP client that
// reads the pages are the researcher's own.
type researcher struct {
	// The shared clients and the lifetime of the manager.
	ctx    context.Context // cancelled when the server stops
	dbCtx  context.Context // the writes must outlive the cancelled agent
	db     *sql.DB
	store  db.Store
	logf   *log.Entry
	client *ai.Client
	mgr    *Manager // for the sub-agents and the messages
	mem    *Memory  // the knowledge base of the agent (nil: none); its sub-agents get the same

	// Its own.
	run  *core.Run // the record of the run, as created
	opts Options
	def  *agent_defs.Def      // the definition it was built from
	reg  *agent_defs.Registry // all the definitions: the roles of def are found in it
	// budgetReason is why the agent's last call was made early (see outOfBudget); "" if it was not. budgetTree says that it was the cap of the
	// whole research, not its own.
	budgetReason string
	budgetTree   bool
	agent        *agent.Agent
	http         *http.Client // reads the pages of this agent only
	cur          *core.State  // the state the run is in
	total        agent.Result // the totals of the agent so far

	// Its place in the tree of agents of a research. A research started by a user is the root: no parent, depth 0.
	parent *researcher
	depth  int
	tree   *tree
	// runCtx is what the agent runs in: the context of the manager for a root, the kidCtx of the parent for a sub-agent, so that a
	// sub-agent is cancelled when its parent ends. kidCtx is what its own sub-agents run in; kidCancel ends them.
	runCtx    context.Context
	kidCtx    context.Context
	kidCancel context.CancelFunc
	// stop ends runCtx (and so the agent and its sub-agents) with the cause *stopCause (Manager.Stop). done is closed when the
	// goroutine of the agent has ended its run.
	stop context.CancelCauseFunc
	done chan struct{}
	// kids are its sub-agents. self is itself as its siblings know it (nil for a root). out is how its run ended.
	kids *family
	self *member
	out  outcome
	// onTrajectory is set when trajectories are recorded (Manager.UseTrajectories). traj and agents are what finishTrajectory made of
	// the trajectory of the agent and its sub-agents.
	onTrajectory TrajectoryFunc
	traj         *atif.Trajectory
	agents       []agentInfo
}

// newResearcher makes the researcher of run (already recorded) with its own agent, built from the definition def and observed by the
// researcher itself. mem is the knowledge base the agent works with, in the dataset of opts (nil: none). parent is the researcher that
// spawns it as a sub-agent (nil for a research started by a user).
func newResearcher(m *Manager, run *core.Run, opts Options, mem *Memory, parent *researcher, def *agent_defs.Def) *researcher {
	r := &researcher{
		ctx:    m.ctx,
		dbCtx:  context.WithoutCancel(m.ctx),
		db:     m.DB,
		store:  m.Ops.Store,
		logf:   log.WithFields(log.Fields{"run_id": run.ID, "user_id": run.UserID}),
		client: m.Client,
		run:    run,
		opts:   opts,
		def:    def,
		reg:    m.registry(),
		http:   agent.NewHTTPClient(),
		mgr:    m,
		mem:    mem,
		parent: parent,
		tree:   &tree{max: def.MaxPerTree(), costCap: opts.treeCostCap},
		kids:   &family{},
		done:   make(chan struct{}),
	}
	parentCtx := m.ctx
	if parent != nil {
		parentCtx = parent.kidCtx
	}
	r.runCtx, r.stop = context.WithCancelCause(parentCtx)
	if parent != nil {
		r.depth, r.tree = parent.depth+1, parent.tree
		r.self = &member{id: run.ID, name: run.Name, reason: run.Reason, done: make(chan struct{})}
	}
	r.kidCtx, r.kidCancel = context.WithCancel(r.runCtx)
	if parent != nil {
		r.reg = parent.reg // the roles of a sub-agent are found among the definitions its parent's research started with
	}
	var kb agent.Knowledge // a nil Memory must become a nil Knowledge
	if mem != nil && def.Knowledge.Access != agent_defs.AccessNone {
		kb = mem.Knowledge(r.ctx, run.UserID, opts.Dataset, SessionID(run.ID))
	}
	r.agent = agent.FromDef(def, r.client, r.http, kb)
	r.agent.Model, r.agent.MaxSteps = opts.Model, opts.MaxSteps
	if parent != nil {
		r.agent.System = prompts.Join(r.agent.System, prompts.Subagent(run.Name, run.Reason))
	}
	// An agent can spawn sub-agents if its definition says so, unless it is at the deepest level.
	agent.AddTeamTools(r.agent, team{r}, r.teamTools())
	agent.AddSkills(r.agent, def.Skills)
	r.agent.OutOfBudget = r.outOfBudget
	r.agent.Observer = agent.Observer{
		BeforeModel: r.beforeModel,
		AfterModel:  func(_ int, t agent.Result) { r.saveUsage(t) },
		BeforeTool:  r.beforeTool,
		AfterTool:   r.afterTool,
	}
	return r
}

// work is the goroutine of the agent: it runs the agent, records every state it enters and what it spends, and ends the run's record.
// A failed write of the progress is logged and does not stop the agent: the research is worth more than its trace.
func (r *researcher) work() {
	defer close(r.done)
	defer r.stop(nil)
	defer r.end()
	defer r.http.CloseIdleConnections()
	r.logf.WithField("topic", truncate(r.run.Topic, 100)).Info("agent research: started")
	defer func() {
		if p := recover(); p != nil {
			r.logf.Errorf("agent research: the agent panicked: %v", p)
			r.finish(core.StatusFailed, "", fmt.Sprintf("internal error: %v", p))
		}
	}()

	// Its sub-agents end with it, and the run is not finished before they are.
	defer r.endKids()

	res, err := r.agent.Run(r.runCtx, r.opts.Topic+r.startRoles())
	if res != nil {
		r.total = *res
	}
	r.endKids()
	switch {
	case r.ctx.Err() != nil:
		r.finish(core.StatusFailed, "", "interrupted: the server stopped while the agent worked")
	case stopCauseOf(r.runCtx) != nil:
		r.finish(core.StatusStopped, "", stopCauseOf(r.runCtx).text(r.run.ID))
	case r.runCtx.Err() != nil:
		r.finish(core.StatusFailed, "", "cancelled: the agent that spawned it ended")
	case err != nil:
		r.logf.WithError(err).Warn("agent research: the agent failed")
		r.finish(core.StatusFailed, "", truncate(err.Error(), maxTextLen))
	case strings.TrimSpace(res.Answer) == "":
		r.finish(core.StatusFailed, "", "the agent ended without a report")
	default:
		r.finish(core.StatusCompleted, res.Answer+r.capNote(res), "")
	}
	r.finishTrajectory(res)
}

// recordTrajectory makes the agent record its run as an ATIF trajectory, if the manager was asked to (r.onTrajectory is set). The
// trajectories of an agent and its sub-agents are merged by finishTrajectory, so none is stored on its own.
func (r *researcher) recordTrajectory() {
	if r.onTrajectory == nil {
		return
	}
	r.agent.Trajectory = true
	r.agent.SessionID = SessionID(r.run.ID)
}

// endKids cancels the sub-agents of the agent and waits until they have ended.
func (r *researcher) endKids() {
	r.kidCancel()
	r.kids.waitAll()
}

// end is the last thing the agent does: its parent, who may wait for it, learns how it ended.
func (r *researcher) end() {
	if r.self == nil {
		return
	}
	r.self.out = r.out
	r.self.traj, r.self.agents = r.traj, r.agents
	close(r.self.done)
}

// tx runs fn in a transaction.
func (r *researcher) tx(fn func(q db.Execer) error) error {
	tx, err := r.db.BeginTx(r.dbCtx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (r *researcher) beforeModel(step int, final bool) {
	if final {
		detail := fmt.Sprintf("step %d of %d, the last: no tools, the answer is the report", step, r.opts.MaxSteps)
		if r.budgetReason != "" {
			detail = fmt.Sprintf("step %d, the last, made early: %s No tools, the answer is the report", step, r.budgetReason)
		}
		r.enter(core.StateWritingReport, step, detail)
		return
	}
	r.enter(core.StateThinking, step, fmt.Sprintf("step %d of %d", step, r.opts.MaxSteps))
}

func (r *researcher) beforeTool(step int, name string, input json.RawMessage) {
	state, detail := toolState(name, input)
	r.enter(state, step, detail)
}

func (r *researcher) afterTool(_ int, _, output string, isErr bool, t agent.Result) {
	errText := ""
	if isErr {
		errText = truncate(output, maxTextLen)
	}
	if r.cur != nil {
		if err := r.store.CloseState(r.dbCtx, r.db, r.cur.ID, errText); err != nil {
			r.logf.WithError(err).Warn("agent research: ending the state failed")
		}
	}
	r.saveUsage(t)
}

// enter moves the run to a new state: it ends the previous one, records the new one and sets the run's state.
func (r *researcher) enter(state string, step int, detail string) {
	detail = truncate(detail, maxTextLen)
	err := r.tx(func(q db.Execer) (err error) {
		r.cur, err = r.store.EnterState(r.dbCtx, q, r.run.ID, state, step, detail)
		return err
	})
	if err != nil {
		r.logf.WithError(err).WithField("state", state).Warn("agent research: recording the state failed")
	}
}

// saveUsage keeps the totals of the agent so far and writes them to the run.
func (r *researcher) saveUsage(t agent.Result) {
	r.tree.addCost(t.CostUSD - r.total.CostUSD)
	r.total = t
	if err := r.store.UpdateUsage(r.dbCtx, r.db, r.run.ID, usageOf(t)); err != nil {
		r.logf.WithError(err).Warn("agent research: recording the usage failed")
	}
}

// finish ends the run's record: the final usage, the status, the report or the error, and the terminal state.
func (r *researcher) finish(status core.Status, report, errText string) {
	r.out = outcome{status: status, report: report, err: errText}
	err := r.tx(func(q db.Execer) error {
		if err := r.store.UpdateUsage(r.dbCtx, q, r.run.ID, usageOf(r.total)); err != nil {
			return err
		}
		_, err := r.store.FinishRun(r.dbCtx, q, r.run.ID, status, report, errText)
		return err
	})
	if err != nil {
		r.logf.WithError(err).Error("agent research: ending the run failed")
	}
	r.logf.WithField("status", status).Info("agent research: ended")
}

// toolState is the state the agent enters when it runs the tool name with input, and its detail.
func toolState(name string, input json.RawMessage) (state, detail string) {
	arg := func(key string) string {
		var m map[string]any
		if json.Unmarshal(input, &m) != nil {
			return ""
		}
		s, _ := m[key].(string)
		return s
	}
	switch name {
	case "web_search":
		return core.StateSearching, arg("query")
	case "fetch_url":
		return core.StateReading, arg("url")
	case "query":
		return core.StateQuerying, arg("question")
	case agent.ToolSpawnSubagent:
		var m struct{ Name, Reason string }
		json.Unmarshal(input, &m)
		return core.StateSpawning, m.Name + ": " + truncate(m.Reason, 200)
	case agent.ToolWaitSubagents:
		var m struct{ Names []string }
		json.Unmarshal(input, &m)
		if len(m.Names) == 0 {
			return core.StateWaiting, "all sub-agents"
		}
		return core.StateWaiting, strings.Join(m.Names, ", ")
	case "remember":
		var m struct {
			Text string `json:"text"`
			Sync bool   `json:"sync"`
		}
		json.Unmarshal(input, &m)
		mode := "async"
		if m.Sync {
			mode = "sync"
		}
		return core.StateRemembering, mode + ": " + truncate(m.Text, 200)
	}
	return core.StateUsingTool, name
}

func usageOf(r agent.Result) db.Usage {
	return db.Usage{Steps: r.Steps, ToolCalls: r.ToolCalls, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CostUSD: r.CostUSD}
}

// truncate cuts s to n characters.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// canSpawn reports whether the agent may start sub-agents: its definition says so and it is not at its deepest level.
func (r *researcher) canSpawn() bool { return r.def.Spawn.Enabled && r.depth < r.def.MaxDepth() }

// teamTools says which tools and which part of the prompt about sub-agents the agent gets.
func (r *researcher) teamTools() agent.TeamTools {
	var tt agent.TeamTools
	if !r.canSpawn() {
		return tt
	}
	if len(r.def.Spawn.Roles) == 0 { // free-form: sub-agents that are copies of the agent
		tt.Spawn = true
		return tt
	}
	// With roles, the model starts the on_demand ones; the on_start ones are started for it (startRoles), and it only waits for them.
	info := prompts.RolesInfo{MaxPerAgent: r.def.MaxPerAgent()}
	for _, role := range r.def.OnDemandRoles() {
		tt.Roles = append(tt.Roles, agent.Role{Name: role.Name, Does: role.Does, When: role.When})
		info.OnDemand = append(info.OnDemand, prompts.Role{Name: role.Name, Does: role.Does, When: role.When, MaxInstances: role.MaxInstances})
	}
	for _, role := range r.def.OnStartRoles() {
		info.OnStart = append(info.OnStart, prompts.Role{Name: role.Name, Does: role.Does})
	}
	tt.Spawn, tt.Wait = len(info.OnDemand) > 0, len(info.OnDemand) == 0
	tt.Prompt = prompts.Roles(info)
	return tt
}

// startRoles starts the sub-agents of the on_start roles of the definition, before the agent's first model call, and returns the note
// for its first turn that says so ("" if there are none). A sub-agent that cannot be started (a limit, the cost cap) is said in the note too.
func (r *researcher) startRoles() string {
	if !r.canSpawn() {
		return ""
	}
	var started, failed []string
	for _, role := range r.def.OnStartRoles() {
		task, err := role.Task(r.opts.Topic)
		if err == nil {
			steps := 0
			if child, derr := r.reg.RoleDef(role); derr == nil {
				steps, _ = role.Steps(child)
			}
			_, err = r.mgr.spawn(r, role.Name, role.Name, role.Does, task, steps)
		}
		if err != nil {
			r.logf.WithError(err).WithField("role", role.Name).Warn("agent research: starting the sub-agent of an on_start role failed")
			failed = append(failed, fmt.Sprintf("%q (%v)", role.Name, err))
			continue
		}
		started = append(started, fmt.Sprintf("%q", role.Name))
	}
	var sb strings.Builder
	if len(started) > 0 {
		fmt.Fprintf(&sb, "\n\n[Sub-agents %s were started for you and are working on parts of this topic in the background. Call wait_subagents to get their reports.]", strings.Join(started, ", "))
	}
	if len(failed) > 0 {
		fmt.Fprintf(&sb, "\n\n[These sub-agents could not be started: %s.]", strings.Join(failed, ", "))
	}
	return sb.String()
}

// outOfBudget is the agent's OutOfBudget: it says why the next model call must be the last, when the cost cap of the whole research or
// the agent's own was reached ("" if not). It is called on the agent's goroutine.
func (r *researcher) outOfBudget() string {
	switch {
	case r.tree.capReached(true):
		r.budgetTree = true
		r.budgetReason = fmt.Sprintf("The cost cap of this research ($%.2f for all the agents together) was reached.", r.tree.costCap)
	case r.def.Limits.MaxCostUSD > 0 && r.total.CostUSD >= r.def.Limits.MaxCostUSD:
		r.budgetReason = fmt.Sprintf("Your cost cap ($%.2f) was reached.", r.def.Limits.MaxCostUSD)
	default:
		return ""
	}
	return r.budgetReason
}

// capNote is the sentence added to the report of an agent that a cost cap cut short, so that whoever reads it knows it may be
// incomplete: for a sub-agent, the cap that stopped it; for the root, also the cap of the research having stopped any of its agents.
func (r *researcher) capNote(res *agent.Result) string {
	switch {
	case res.OutOfBudget != "" && r.budgetTree:
		return fmt.Sprintf("\n\n---\n*Stopped early: the cost cap of this research ($%.2f) was reached, so this report is based on what had been gathered until then and may be incomplete.*", r.tree.costCap)
	case res.OutOfBudget != "":
		return fmt.Sprintf("\n\n---\n*Stopped early: this agent's cost cap ($%.2f) was reached, so this report is based on what it had gathered until then and may be incomplete.*", r.def.Limits.MaxCostUSD)
	case r.parent == nil && r.tree.capHit():
		return fmt.Sprintf("\n\n---\n*Stopped early: the cost cap of this research ($%.2f) was reached while it worked, so some of its agents stopped early and this report may be incomplete.*", r.tree.costCap)
	}
	return ""
}
