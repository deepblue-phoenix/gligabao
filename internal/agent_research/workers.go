package agent_research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent/prompts"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate_core"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	log "github.com/sirupsen/logrus"
)

// outcome is how a run ended.
type outcome struct {
	status core.Status
	report string
	err    string
}

// member is a worker as its supervisor knows it. Only id, name and reason (which never change) are read before the
// worker has ended; out is written before done is closed, and read after.
type member struct {
	id     int64
	name   string
	reason string
	done   chan struct{}
	out    outcome
	// traj is the trajectory of the worker, and agents the entries of all of them; set
	// (with out) before done is closed, and only if trajectories are recorded.
	traj   *atif.Trajectory
	agents []agentInfo
}

// family is the workers of one supervisor, in the order they were started.
type family struct {
	mu      sync.Mutex
	members []*member
}

// tree is what the agents of one research (the root and the workers below it) share: the count of the workers and the money spent.
type tree struct {
	mu      sync.Mutex
	spawned int
	// max is the most workers in the run (the max_workers of the root's knobs; 0 is agent_defs.DefaultMaxWorkers).
	max int
	// costCap is the most the whole research may spend, in USD (0: no cap); cost is what its agents have spent so far; hit says that an
	// agent was cut short, or a worker refused, because of the cap.
	costCap float64
	cost    float64
	hit     bool
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

func (t *tree) reserve() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	max := t.max
	if max <= 0 {
		max = agent_defs.DefaultMaxWorkers
	}
	if t.spawned >= max {
		return false
	}
	t.spawned++
	return true
}

func (t *tree) limit() int {
	if t.max <= 0 {
		return agent_defs.DefaultMaxWorkers
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
// research something (an agent was cut short or a worker refused), which the report of the root says.
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

// workerPool is the investigate.Workers of one supervisor: it runs a worker (a sub-agent, a run with parent_id) for each question it is given.
type workerPool struct {
	r *researcher
	// steps is the model calls of a worker.
	steps int
}

var _ investigate.Workers = workerPool{}

// Run starts the worker of the question, waits for it and returns its findings and what it spent.
func (p workerPool) Run(ctx context.Context, req investigate.WorkerRequest) (investigate.WorkerResult, error) {
	reason := "question " + req.QuestionID + ": " + truncate(req.Question, 250)
	w, err := p.r.mgr.startWorker(p.r, req.QuestionID, reason, req.Brief, p.steps, req.MaxCostUSD)
	if err != nil {
		return investigate.WorkerResult{}, err
	}
	select {
	case <-w.self.done:
	case <-ctx.Done():
		<-w.self.done // it is cancelled with the supervisor: wait for its record to be closed
		return investigate.WorkerResult{}, ctx.Err()
	}
	res := investigate.WorkerResult{
		InputTokens:  w.total.InputTokens,
		OutputTokens: w.total.OutputTokens,
		CostUSD:      w.total.CostUSD,
	}
	if out := w.self.out; out.status != core.StatusCompleted {
		return res, fmt.Errorf("the worker %s did not finish: %s", w.self.name, out.err)
	}
	if res.Findings, err = investigate_core.ParseFindings(json.RawMessage(w.self.out.report)); err != nil {
		return res, err
	}
	return res, nil
}

// startWorker records a worker of parent (a run with parent_id and a name that starts with name) and starts it in the background, on its own
// goroutine like every agent. costCap is the most it should spend (0: no cap of its own). It is called on the goroutine of the supervisor, from the fan-out of its loop, possibly several times at once.
func (m *Manager) startWorker(parent *researcher, name, reason, task string, maxSteps int, costCap float64) (*researcher, error) {
	switch {
	case strings.TrimSpace(task) == "":
		return nil, errors.New("task is empty")
	case maxSteps < 1 || maxSteps > MaxMaxSteps:
		return nil, fmt.Errorf("max_steps must be between 1 and %d", MaxMaxSteps)
	case m.ctx.Err() != nil:
		return nil, errors.New("the server is shutting down")
	}
	kids := parent.kids
	kids.mu.Lock() // the choice of the name, the record and the entry in the family are one step
	defer kids.mu.Unlock()
	base := name
	for i := 2; slices.ContainsFunc(kids.members, func(k *member) bool { return k.name == name }); i++ {
		name = fmt.Sprintf("%s-%d", base, i) // a question that goes to a worker again
	}
	if parent.tree.capReached(true) {
		return nil, fmt.Errorf("the cost cap of this research ($%.2f) was reached: no more workers", parent.tree.costCap)
	}
	if !parent.tree.reserve() {
		return nil, fmt.Errorf("this research has %d workers already, the most it may have", parent.tree.limit())
	}
	ok := false
	defer func() {
		if !ok {
			parent.tree.release()
		}
	}()

	def := parent.def
	parentID := parent.run.ID
	run := &core.Run{
		UserID:       parent.run.UserID,
		ParentID:     &parentID,
		Name:         name,
		Reason:       strings.TrimSpace(reason),
		Topic:        strings.TrimSpace(task),
		Status:       core.StatusRunning,
		State:        core.StateStarting,
		Model:        parent.opts.Model,
		MaxSteps:     maxSteps,
		AgentName:    def.Name,
		AgentVersion: def.Version,
		AgentHash:    def.Hash,
	}
	dbCtx := context.WithoutCancel(m.ctx)
	err := m.tx(dbCtx, func(q db.Execer) error {
		if err := m.Ops.Store.CreateRun(dbCtx, q, run); err != nil {
			return err
		}
		_, err := m.Ops.Store.EnterState(dbCtx, q, run.ID, core.StateStarting, 0, "")
		return err
	})
	if err != nil {
		log.WithError(err).WithField("parent_id", parentID).Error("agent research: recording the worker failed")
		return nil, errors.New("could not record the worker")
	}
	opts := Options{
		Topic:    run.Topic,
		MaxSteps: maxSteps,
		Model:    parent.opts.Model,
		Dataset:  parent.opts.Dataset,
		Agent:    def.Name,
	}
	sub := newResearcher(m, run, opts, parent.mem, parent, def)
	sub.costCap = costCap
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

// workerDef is the definition a worker runs: the one of its supervisor, with the prompt of a worker in place of its own.
func workerDef(d *agent_defs.Def) *agent_defs.Def {
	w := *d
	w.SystemPrompt = prompts.Worker
	return &w
}

// workerAgent makes the agent of a worker: the tools of the definition and the knowledge base, a minimal prompt, and submit_findings as the
// terminal tool.
func workerAgent(r *researcher, kb agent.Knowledge) *agent.Agent {
	a := agent.FromDef(workerDef(r.def), r.client, r.http, kb)
	a.ErrSink = r.mgr.errSink()
	a = withWorkerCode(r, a)
	a.Tools = append(a.Tools, agent.SubmitFindingsTool(investigate_core.FindingsSchema(), func(raw json.RawMessage) error {
		_, err := investigate_core.ParseFindings(raw)
		return err
	}))
	a.Terminal = agent.ToolSubmitFindings
	return a
}

// withWorkerCode gives the worker the run_code tool when the definition has [code] on and the sandbox is available (like its supervisor's).
func withWorkerCode(r *researcher, a *agent.Agent) *agent.Agent {
	cfg, err := r.codeConfig()
	if err != nil || cfg == nil {
		if err != nil {
			r.logf.WithError(err).Warn("agent research: the worker runs without run_code")
		}
		return a
	}
	tool, ok, err := investigate.WorkerCodeTool(cfg, a.Tools)
	if err != nil || !ok {
		if err != nil {
			r.logf.WithError(err).Warn("agent research: the worker runs without run_code")
		}
		return a
	}
	a.Tools = append(a.Tools, tool)
	a.System = prompts.Join(a.System, investigate.WorkerCodeNote)
	return a
}
