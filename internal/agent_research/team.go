package agent_research

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	log "github.com/sirupsen/logrus"
)

// Limits of the sub-agents. They bound what one research can cost: a sub-agent works like any other agent, with up to dozens of
// paid model calls. Sub-agents are not counted in the DefaultMaxActivePerUser of the user's researches; these limits bound them.
const (
	// MaxSubagentDepth is how many generations of sub-agents a research may have: its sub-agents may spawn their own, whose agents
	// may not spawn any more (with 2).
	MaxSubagentDepth = agent_defs.MaxSubagentDepth
	// MaxSubagentsPerAgent is the most sub-agents one agent spawns, and MaxSubagentsPerTree the most in the whole tree of a research.
	// They are ceilings: the definition of an agent may lower them (its Spawn limits), never raise them.
	MaxSubagentsPerAgent = agent_defs.MaxSubagentsPerAgent
	MaxSubagentsPerTree  = agent_defs.MaxSubagentsPerTree
	// DefaultSubagentMaxSteps is the number of model calls a free-form sub-agent (one spawned without a role) may make when its parent
	// says none; a role has its own default.
	DefaultSubagentMaxSteps = agent_defs.DefaultSubagentSteps
)

// outcome is how a run ended.
type outcome struct {
	status core.Status
	report string
	err    string
}

// member is a sub-agent as its parent knows it. Only id, name and reason (which never change) are read before the
// sub-agent has ended; out is written before done is closed, and read after.
type member struct {
	id     int64
	name   string
	role   string // the role of the parent's definition it was spawned for ("" free-form)
	reason string
	done   chan struct{}
	out    outcome
	// traj is the trajectory of the sub-agent with those of its own sub-agents merged in, and agents the entries of all of them; set
	// (with out) before done is closed, and only if trajectories are recorded.
	traj   *atif.Trajectory
	agents []agentInfo
}

// ended reports whether the sub-agent has ended.
func (m *member) ended() bool {
	select {
	case <-m.done:
		return true
	default:
		return false
	}
}

// family is the sub-agents of one parent, in the order they were spawned.
type family struct {
	mu      sync.Mutex
	members []*member
}

func (f *family) get(name string) *member {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.members {
		if m.name == name {
			return m
		}
	}
	return nil
}

func (f *family) all() []*member {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*member(nil), f.members...)
}

func (f *family) names(except string) string {
	var names []string
	for _, m := range f.all() {
		if m.name != except {
			names = append(names, m.name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// waitAll blocks until all the members have ended.
func (f *family) waitAll() {
	for _, m := range f.all() {
		<-m.done
	}
}

// tree is what the agents of one research (the root and everything below it) share: the count of the sub-agents and the money spent.
type tree struct {
	mu      sync.Mutex
	spawned int
	// max is the most sub-agents in the tree (the limit of the root's definition; 0 is MaxSubagentsPerTree).
	max int
	// costCap is the most the whole research may spend, in USD (0: no cap); cost is what its agents have spent so far; hit says that an
	// agent was cut short, or a sub-agent refused, because of the cap.
	costCap float64
	cost    float64
	hit     bool
}

func (t *tree) reserve() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	max := t.max
	if max <= 0 {
		max = MaxSubagentsPerTree
	}
	if t.spawned >= max {
		return false
	}
	t.spawned++
	return true
}

func (t *tree) limit() int {
	if t.max <= 0 {
		return MaxSubagentsPerTree
	}
	return t.max
}

func (t *tree) release() {
	t.mu.Lock()
	t.spawned--
	t.mu.Unlock()
}

// addCost adds what an agent spent since it last told.
func (t *tree) addCost(usd float64) {
	if usd == 0 {
		return
	}
	t.mu.Lock()
	t.cost += usd
	t.mu.Unlock()
}

// capReached reports whether the research has spent what its cap allows; with markHit it also records that the cap has cost the
// research something (an agent was cut short or a sub-agent refused), which the report of the root says.
func (t *tree) capReached(markHit bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	reached := t.costCap > 0 && t.cost >= t.costCap
	if reached && markHit {
		t.hit = true
	}
	return reached
}

// capHit reports whether the cap cut the research short.
func (t *tree) capHit() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.hit
}

// team is the view of one agent of the sub-agents (agent.Team).
type team struct{ r *researcher }

var _ agent.Team = team{}

// Spawn starts a sub-agent of the agent.
func (t team) Spawn(_ context.Context, role, name, reason, task string, maxSteps int) (string, error) {
	r := t.r
	if !r.canSpawn() {
		return "", fmt.Errorf("you cannot spawn sub-agents: sub-agents go at most %d levels deep", r.def.MaxDepth())
	}
	child, err := r.mgr.spawn(r, role, name, reason, task, maxSteps)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("started sub-agent %q (run %d): it works in the background; call wait_subagents to get its report", name, child.run.ID), nil
}

// Wait waits for the sub-agents and returns their reports.
func (t team) Wait(ctx context.Context, names []string) (string, error) {
	r := t.r
	var wanted []*member
	if len(names) == 0 {
		if wanted = r.kids.all(); len(wanted) == 0 {
			return "", errors.New("you have no sub-agents: spawn some with spawn_subagent first")
		}
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		m := r.kids.get(name)
		if m == nil {
			return "", fmt.Errorf("you have no sub-agent called %q (yours: %s)", name, r.kids.names(""))
		}
		wanted = append(wanted, m)
	}
	var sb strings.Builder
	for i, m := range wanted {
		select {
		case <-m.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "## %s (run %d): %s\n", m.name, m.id, m.out.status)
		if m.out.status == core.StatusCompleted {
			sb.WriteString(m.out.report)
		} else {
			fmt.Fprintf(&sb, "It did not finish: %s", m.out.err)
		}
	}
	return sb.String(), nil
}

// spawn records a sub-agent of parent (a run with parent_id and name) and starts it in the background, on its own goroutine like
// every agent. role is the role of the parent's definition the sub-agent is for: it decides the definition the sub-agent runs ("" when
// the definition has no roles: the sub-agent is a copy of the parent). maxSteps 0 is the default of the role. It is called on the
// goroutine of parent, by its tool (or by the start of the parent, for an on_start role). Errors are shown to the model of the parent.
func (m *Manager) spawn(parent *researcher, role, name, reason, task string, maxSteps int) (*researcher, error) {
	child, maxSteps, err := m.childDef(parent, role, maxSteps)
	if err != nil {
		return nil, err
	}
	switch {
	case !agent.ValidSubagentName(name):
		return nil, errors.New("name must be 1 to 40 letters, digits, - or _")
	case strings.TrimSpace(reason) == "":
		return nil, errors.New("reason is empty: say why this sub-agent has to exist")
	case strings.TrimSpace(task) == "":
		return nil, errors.New("task is empty")
	case maxSteps < 1 || maxSteps > MaxMaxSteps:
		return nil, fmt.Errorf("max_steps must be between 1 and %d", MaxMaxSteps)
	case m.ctx.Err() != nil:
		return nil, errors.New("the server is shutting down")
	}
	kids := parent.kids
	kids.mu.Lock() // the name check, the record and the entry in the family are one step
	defer kids.mu.Unlock()
	instances := 0
	for _, k := range kids.members {
		if k.name == name {
			return nil, fmt.Errorf("you have a sub-agent called %q already: choose another name", name)
		}
		if role != "" && k.role == role {
			instances++
		}
	}
	if limit := parent.def.MaxPerAgent(); len(kids.members) >= limit {
		return nil, fmt.Errorf("you have %d sub-agents already, the most one agent may have", limit)
	}
	if rc, ok := parent.def.Role(role); ok && rc.MaxInstances > 0 && instances >= rc.MaxInstances {
		return nil, fmt.Errorf("you have %d sub-agents of the role %q already, the most you may have", instances, role)
	}
	if parent.tree.capReached(true) {
		return nil, fmt.Errorf("the cost cap of this research ($%.2f) was reached: you cannot start more sub-agents; go on with what you have", parent.tree.costCap)
	}
	if !parent.tree.reserve() {
		return nil, fmt.Errorf("this research has %d sub-agents already, the most it may have", parent.tree.limit())
	}
	ok := false
	defer func() {
		if !ok {
			parent.tree.release()
		}
	}()

	// The sub-agent runs on the model of its own definition, else on its parent's, and in the dataset of its own definition, else its parent's.
	model := child.Model
	if model == "" {
		model = parent.opts.Model
	}
	dataset := parent.opts.Dataset
	if child.Knowledge.Dataset != "" {
		dataset = child.Knowledge.Dataset
	}
	parentID := parent.run.ID
	run := &core.Run{
		UserID: parent.run.UserID, ParentID: &parentID, Name: name, Reason: strings.TrimSpace(reason), Topic: strings.TrimSpace(task),
		Status: core.StatusRunning, State: core.StateStarting, Model: model, MaxSteps: maxSteps,
		AgentName: child.Name, AgentVersion: child.Version, AgentHash: child.Hash,
	}
	dbCtx := context.WithoutCancel(m.ctx)
	err = m.tx(dbCtx, func(q db.Execer) error {
		if err := m.Ops.Store.CreateRun(dbCtx, q, run); err != nil {
			return err
		}
		_, err := m.Ops.Store.EnterState(dbCtx, q, run.ID, core.StateStarting, 0, "")
		return err
	})
	if err != nil {
		log.WithError(err).WithField("parent_id", parentID).Error("agent research: recording the sub-agent failed")
		return nil, errors.New("could not record the sub-agent")
	}
	opts := Options{Topic: run.Topic, MaxSteps: maxSteps, Model: model, Dataset: dataset, Agent: child.Name}
	sub := newResearcher(m, run, opts, parent.mem, parent, child)
	sub.self.role = role
	sub.onTrajectory = parent.onTrajectory
	sub.recordTrajectory()
	kids.members = append(kids.members, sub.self)
	m.track(sub)
	ok = true

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.untrack(sub)
		sub.work()
	}()
	return sub, nil
}

// childDef finds the definition of the sub-agent that parent spawns for role, and its steps (maxSteps, or the default of the role).
func (m *Manager) childDef(parent *researcher, role string, maxSteps int) (*agent_defs.Def, int, error) {
	pd := parent.def
	if role == "" {
		if len(pd.Spawn.Roles) > 0 {
			return nil, 0, fmt.Errorf("role is required: choose one of your roles (%s)", roleNames(pd))
		}
		if maxSteps == 0 {
			maxSteps = DefaultSubagentMaxSteps
		}
		return pd, maxSteps, nil
	}
	rc, ok := pd.Role(role)
	if !ok {
		return nil, 0, fmt.Errorf("unknown role %q (yours: %s)", role, roleNames(pd))
	}
	child, err := parent.reg.RoleDef(rc)
	if err != nil {
		return nil, 0, err
	}
	def, limit := rc.Steps(child)
	switch {
	case maxSteps == 0:
		maxSteps = def
	case maxSteps > limit:
		return nil, 0, fmt.Errorf("max_steps of the role %q is at most %d", role, limit)
	}
	return child, maxSteps, nil
}

func roleNames(d *agent_defs.Def) string {
	names := make([]string, len(d.Spawn.Roles))
	for i, r := range d.Spawn.Roles {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}
