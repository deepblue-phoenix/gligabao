// Package agent_research runs the deep-research agent (package agent) as a job: a Manager starts a research on a topic in the
// background, records the run in the database (gb_agent_research_runs) and every state the agent goes through while it works
// (gb_agent_research_states: searching, reading, thinking, ...), so that its progress can be followed in detail, and lists and reads
// the runs. The HTTP handlers are in internal/api/research, the MCP tools in internal/api/mcp and the commands in package cli.
package agent_research

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	log "github.com/sirupsen/logrus"
)

// DefaultMaxActivePerUser is the number of agents of one user that may work at the same time: each makes up to dozens of paid model
// calls and web searches.
const DefaultMaxActivePerUser = 3

// maxTextLen cuts the details and errors that are stored (the query the model chose, an error from a web page, ...).
const maxTextLen = 1000

// Manager starts research agents and runs them in the background, until its context is done. It also lists and reads the runs.
// Runs that were still working when the server last stopped are marked failed when the manager is made.
type Manager struct {
	// DB holds the runs and their states.
	DB *sql.DB
	// Client is the LLM client the agents use (nil: starting a research is not possible).
	Client *ai.Client
	// Ops is how the runs and states are stored and read.
	Ops Ops
	// MaxActivePerUser is how many agents of one user may work at once (DefaultMaxActivePerUser if 0). The sub-agents that the agents
	// spawn are not counted: they are bounded by MaxSubagentsPerAgent, MaxSubagentsPerTree and MaxSubagentDepth.
	MaxActivePerUser int
	// MaxCostUSD is the most one research may spend, all its agents together, whatever the definition and the request say (0: no ceiling
	// of the server). Set it before starting any agent.
	MaxCostUSD float64

	ctx context.Context
	mem *Memory // the knowledge base of the agents (nil: they have only the web tools); guarded by mu
	// onTrajectory, when set (UseTrajectories), makes the agents record ATIF trajectories and gets the merged one of each research;
	// guarded by mu.
	onTrajectory TrajectoryFunc
	// remote runs the researches started with the Modal runner (nil: they cannot be started); guarded by mu.
	remote Remote
	// importer loads what the agents of those researches hand back (nil: it is not imported); guarded by mu.
	importer Importer
	// defs are the agent definitions the researches can run (nil: only the built-in default); guarded by mu.
	defs *agent_defs.Registry

	mu      sync.Mutex
	active  map[string]int        // running agents by user
	runs    map[int64]*researcher // the agents that are working, by run ID, to stop them (sub-agents too)
	remotes map[int64]*remoteRun  // the researches that run in Modal, by run ID
	wg      sync.WaitGroup
}

// ManagerOption customizes NewManager.
type ManagerOption func(*managerOptions)

type managerOptions struct{ keepActive bool }

// KeepActiveRuns makes NewManager leave the runs that are marked running alone. By default the manager takes every such run for one
// that a restart interrupted and marks it failed, which is right for the server (the only process that runs agents on its database)
// but wrong for a process that shares the database with others that are working, like several runs of the standalone
// cmd/gligabao_agent on one PostgreSQL database or next to a server: it would fail the runs of the others.
func KeepActiveRuns() ManagerOption { return func(o *managerOptions) { o.keepActive = true } }

// NewManager makes the manager of the research agents whose runs are kept in d. client runs the agents (may be nil: Start then fails
// with KindUnavailable). The agents stop when ctx is done. Runs that a restart interrupted (still running in the database) are marked
// failed, unless KeepActiveRuns is given.
func NewManager(ctx context.Context, d *sql.DB, ops Ops, client *ai.Client, opts ...ManagerOption) *Manager {
	var o managerOptions
	for _, opt := range opts {
		opt(&o)
	}
	m := &Manager{DB: d, Client: client, Ops: ops, ctx: ctx, active: map[string]int{}, runs: map[int64]*researcher{}, remotes: map[int64]*remoteRun{}}
	if o.keepActive {
		return m
	}
	if k, err := ops.Store.FailActive(ctx, d, "interrupted: the server stopped while the agent worked"); err != nil {
		log.WithError(err).Warn("agent research: could not mark interrupted runs as failed")
	} else if k > 0 {
		log.Infof("agent research: %d run(s) were interrupted by a restart and marked failed", k)
	}
	return m
}

// UseMemory gives the agents started from now on the internal knowledge base: the tools remember and query, besides web_search and
// fetch_url. With nil (the default) they have no knowledge base. Agents already working are not changed.
func (m *Manager) UseMemory(mem *Memory) {
	m.mu.Lock()
	m.mem = mem
	m.mu.Unlock()
}

// UseAgentDefs makes the definitions of reg the agents that a research can run (Options.Agent), and those that its agents spawn as
// sub-agents (the roles of a definition). Researches already working are not changed. It checks that the datasets the definitions name
// are valid, and returns the error of the first that is not. Without it only the built-in default agent is available.
func (m *Manager) UseAgentDefs(reg *agent_defs.Registry) error {
	for _, d := range reg.List() {
		if d.Knowledge.Dataset == "" {
			continue
		}
		if _, err := memory_core.ResolveDatasetName(d.Knowledge.Dataset); err != nil {
			return fmt.Errorf("agent %q (%s): knowledge.dataset: %w", d.Name, d.Source, err)
		}
	}
	for _, d := range reg.List() {
		if d.Schedule == nil {
			continue
		}
		if _, err := memory_core.ResolveUserID(d.Schedule.User); err != nil {
			return fmt.Errorf("agent %q (%s): schedule.user: %w", d.Name, d.Source, err)
		}
	}
	m.mu.Lock()
	m.defs = reg
	m.mu.Unlock()
	return nil
}

// builtinDefs is the registry of a manager that was given none.
var builtinDefs = sync.OnceValue(agent_defs.BuiltinRegistry)

// registry returns the definitions in use.
func (m *Manager) registry() *agent_defs.Registry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.defs == nil {
		return builtinDefs()
	}
	return m.defs
}

// AgentInfo describes an agent definition that a research can run.
type AgentInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	// Hash is the SHA-256 of the definition's content; Source the file it was loaded from ("builtin" for the built-in agent).
	Hash   string `json:"hash"`
	Source string `json:"source"`
	// Model, MaxSteps and the caps are the definition's settings; Subagents lists the roles it starts sub-agents for.
	Model          string   `json:"model"`
	MaxSteps       int      `json:"max_steps"`
	MaxCostUSD     float64  `json:"max_cost_usd"`
	MaxTreeCostUSD float64  `json:"max_tree_cost_usd"`
	Knowledge      string   `json:"knowledge"`
	Skills         []string `json:"skills"`
	Subagents      []string `json:"subagents"`
	// Schedule is when the server runs the agent by itself (nil: it does not).
	Schedule *ScheduleInfo `json:"schedule,omitempty"`
}

// ScheduleInfo is the schedule of an agent definition; NextRun is when it runs next (nil if never, or if it is paused).
type ScheduleInfo struct {
	Cron     string     `json:"cron"`
	Timezone string     `json:"timezone"`
	Topic    string     `json:"topic"`
	User     string     `json:"user"`
	Paused   bool       `json:"paused"`
	NextRun  *time.Time `json:"next_run,omitempty"`
}

// Agents lists the agent definitions, by name.
func (m *Manager) Agents() []AgentInfo {
	var out []AgentInfo
	for _, d := range m.registry().List() {
		info := AgentInfo{
			Name: d.Name, Version: d.Version, Description: d.Description, Hash: d.Hash, Source: d.Source, Model: d.Model, MaxSteps: d.MaxSteps,
			MaxCostUSD: d.Limits.MaxCostUSD, MaxTreeCostUSD: d.Limits.MaxTreeCostUSD, Knowledge: d.Knowledge.Access,
			Skills: []string{}, Subagents: []string{},
		}
		if sc := d.Schedule; sc != nil {
			info.Schedule = &ScheduleInfo{Cron: sc.Cron, Timezone: sc.Timezone, Topic: sc.Topic, User: sc.User, Paused: sc.Paused}
			if next := sc.Next(time.Now()); !sc.Paused && !next.IsZero() {
				info.Schedule.NextRun = &next
			}
		}
		for _, s := range d.Skills {
			info.Skills = append(info.Skills, s.Name)
		}
		for _, r := range d.Spawn.Roles {
			info.Subagents = append(info.Subagents, fmt.Sprintf("%s (%s, %s)", r.Name, r.Agent, r.Trigger))
		}
		out = append(out, info)
	}
	return out
}

// applyDef fills in what the definition decides for options that do not say: the model, the steps, the dataset, and the cap on the cost of
// the whole research (the lowest of the request's, the definition's and the server's). KindInvalid for a dataset the definition locks.
func (m *Manager) applyDef(opts Options, def *agent_defs.Def) (Options, error) {
	opts.Agent = def.Name
	if opts.Model == "" {
		opts.Model = def.Model
	}
	if opts.MaxSteps == 0 {
		opts.MaxSteps = def.MaxSteps
	}
	if k := def.Knowledge; k.Dataset != "" {
		switch {
		case k.DatasetLocked && opts.datasetGiven && opts.Dataset != k.Dataset:
			return opts, invalidErr(fmt.Sprintf("the agent %q works in the dataset %q only", def.Name, k.Dataset))
		case k.DatasetLocked || !opts.datasetGiven:
			opts.Dataset = k.Dataset
		}
	}
	opts.treeCostCap = lowestCap(def.Limits.MaxTreeCostUSD, opts.MaxCostUSD, m.MaxCostUSD)
	return opts, nil
}

// lowestCap is the smallest of the caps that are set (positive); 0 if none is.
func lowestCap(caps ...float64) float64 {
	low := 0.0
	for _, c := range caps {
		if c > 0 && (low == 0 || c < low) {
			low = c
		}
	}
	return low
}

// Wait blocks until the running agents have ended (after the manager's context is done, or for tests).
func (m *Manager) Wait() { m.wg.Wait() }

// Start records a research of opts.Topic for userID (already validated), starts the agent in the background and returns the recorded
// run (status running, state starting). Errors are *Error: KindInvalid for bad options, KindUnavailable without an LLM, KindConflict
// if the user has too many agents working, KindInternal.
func (m *Manager) Start(ctx context.Context, userID string, opts Options) (*core.Run, error) {
	opts, err := opts.resolve()
	if err != nil {
		return nil, err
	}
	def, err := m.registry().Resolve(opts.Agent, opts.AgentVersion)
	if err != nil {
		return nil, invalidErr(err.Error())
	}
	if opts, err = m.applyDef(opts, def); err != nil {
		return nil, err
	}
	m.mu.Lock()
	remote := m.remote
	m.mu.Unlock()
	switch {
	case opts.Runner == core.RunnerModal && remote == nil:
		// The agent runs in Modal with the API keys of the Modal app: this server needs no LLM client of its own for it.
		return nil, unavailableErr("the modal runner is not available: it needs the sidecar, with the agent deployed to Modal (see docs/research.md)")
	case opts.Runner != core.RunnerModal && m.Client == nil:
		return nil, unavailableErr("no LLM is available for the research agent (set ANTHROPIC_API_KEY)")
	}
	if m.ctx.Err() != nil {
		return nil, unavailableErr("the server is shutting down")
	}

	limit := m.MaxActivePerUser
	if limit <= 0 {
		limit = DefaultMaxActivePerUser
	}
	m.mu.Lock()
	if m.active[userID] >= limit {
		m.mu.Unlock()
		return nil, conflictErr(fmt.Sprintf("you have %d research agents working already: wait for one to end", limit))
	}
	m.active[userID]++
	mem, onTrajectory := m.mem, m.onTrajectory
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		if m.active[userID]--; m.active[userID] <= 0 {
			delete(m.active, userID)
		}
		m.mu.Unlock()
	}
	if opts.Runner == core.RunnerModal {
		return m.startRemote(ctx, userID, opts, def, remote, release)
	}

	run := &core.Run{UserID: userID, Topic: opts.Topic, Status: core.StatusRunning, State: core.StateStarting, Model: opts.Model, MaxSteps: opts.MaxSteps,
		AgentName: def.Name, AgentVersion: def.Version, AgentHash: def.Hash}
	dbCtx := context.WithoutCancel(ctx)
	err = m.tx(dbCtx, func(q db.Execer) error {
		if err := m.Ops.Store.CreateRun(dbCtx, q, run); err != nil {
			return err
		}
		_, err := m.Ops.Store.EnterState(dbCtx, q, run.ID, core.StateStarting, 0, "")
		return err
	})
	if err != nil {
		release()
		log.WithError(err).WithField("user_id", userID).Error("agent research: recording the run failed")
		return nil, internalErr("failed to record the research", err)
	}
	rec := *run // what the caller gets; the researcher owns run from here on

	// Each agent gets a researcher (all of its state) and a goroutine of its own.
	r := newResearcher(m, run, opts, mem, nil, def)
	r.onTrajectory = onTrajectory
	r.recordTrajectory()
	m.track(r)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer release()
		defer m.untrack(r)
		r.work()
	}()
	return &rec, nil
}

// track and untrack keep the registry of the agents that are working (see Stop).
func (m *Manager) track(r *researcher) {
	m.mu.Lock()
	m.runs[r.run.ID] = r
	m.mu.Unlock()
}

func (m *Manager) untrack(r *researcher) {
	m.mu.Lock()
	delete(m.runs, r.run.ID)
	m.mu.Unlock()
}

// stopWait is how long Stop waits for the agent to end its run.
const stopWait = 10 * time.Second

// Stop stops the agent of run id: its context is cancelled, so its model call or tool is interrupted, and so are its sub-agents,
// which end the same way. Its run ends as StatusStopped, with error saying by (who asked, "the admin UI" say), and no report. Stop
// returns when the run has ended (or after a few seconds, when the run may still be ending), with the run as it is then. userID is
// the owner ("" matches every owner, for internal use: the admin UI). KindNotFound if there is no such run, KindConflict if it
// has ended already.
func (m *Manager) Stop(ctx context.Context, userID string, id int64, by string) (*core.Run, error) {
	m.mu.Lock()
	r := m.runs[id]
	rr := m.remotes[id]
	m.mu.Unlock()
	if rr != nil {
		return m.stopRemote(ctx, userID, rr, by)
	}
	if r == nil {
		run, err := m.Get(ctx, userID, id) // not found, or not the owner's
		if err != nil {
			return nil, err
		}
		return nil, conflictErr(fmt.Sprintf("research %d is not working (its status is %s)", run.ID, run.Status))
	}
	if userID != "" && r.run.UserID != userID {
		return nil, notFoundErr("research not found")
	}
	r.stop(&stopCause{runID: id, by: by})
	select {
	case <-r.done:
	case <-time.After(stopWait):
	case <-ctx.Done():
	}
	run, err := m.Ops.Store.GetRun(context.WithoutCancel(ctx), m.DB, id)
	if err != nil {
		return nil, internalErr("failed to read the research", err)
	}
	return run, nil
}

// stopRemote is Stop for a research that runs in Modal: the job is asked to end, and the run ends as stopped when it has.
func (m *Manager) stopRemote(ctx context.Context, userID string, rr *remoteRun, by string) (*core.Run, error) {
	if userID != "" && rr.run.UserID != userID {
		return nil, notFoundErr("research not found")
	}
	rr.requestStop(ctx, by)
	select {
	case <-rr.done:
	case <-time.After(stopWait):
	case <-ctx.Done():
	}
	run, err := m.Ops.Store.GetRun(context.WithoutCancel(ctx), m.DB, rr.run.ID)
	if err != nil {
		return nil, internalErr("failed to read the research", err)
	}
	return run, nil
}

// stopCause is the cause of the cancelled context of an agent that Stop stopped. The sub-agents of the agent see it too.
type stopCause struct {
	runID int64  // the run that was stopped
	by    string // who asked
}

func (c *stopCause) Error() string { return fmt.Sprintf("research %d stopped by %s", c.runID, c.by) }

// text is the error recorded with the stopped run id: who stopped it, or that its parent was stopped.
func (c *stopCause) text(id int64) string {
	if c.runID == id {
		return "stopped by " + c.by
	}
	return fmt.Sprintf("stopped: the agent that spawned it (run %d) was stopped by %s", c.runID, c.by)
}

// stopCauseOf is the stopCause that ended ctx, or nil if it was not ended by Stop.
func stopCauseOf(ctx context.Context) *stopCause {
	var c *stopCause
	if errors.As(context.Cause(ctx), &c) {
		return c
	}
	return nil
}

// Get returns run id with its states. userID is its owner: the run of another user is not found, as if it did not exist ("" matches
// every owner, for internal use). KindNotFound if there is no such run.
func (m *Manager) Get(ctx context.Context, userID string, id int64) (*core.Run, error) {
	run, err := m.Ops.Store.GetRun(ctx, m.DB, id)
	if errors.Is(err, db.ErrNotFound) || (err == nil && userID != "" && run.UserID != userID) {
		return nil, notFoundErr("research not found")
	}
	if err != nil {
		return nil, internalErr("failed to read the research", err)
	}
	if run.States, err = m.Ops.Store.ListStates(ctx, m.DB, id); err != nil {
		return nil, internalErr("failed to read the research", err)
	}
	return run, nil
}

// List returns the runs of userID (of every user for ""), newest first, without their states. KindInvalid for bad options.
func (m *Manager) List(ctx context.Context, userID string, opts ListOptions) ([]core.Run, error) {
	f := core.RunFilter{UserID: userID, RootsOnly: opts.RootsOnly}
	if opts.ParentID != 0 {
		f.ParentID = &opts.ParentID
	}
	switch {
	case (opts.Active && opts.Status != "") || (opts.Finished && (opts.Active || opts.Status != "")):
		return nil, invalidErr("give active, finished or status, not more than one")
	case opts.Finished:
		for _, s := range core.Statuses {
			if !s.Active() {
				f.Statuses = append(f.Statuses, s)
			}
		}
	case opts.Active:
		for _, s := range core.Statuses {
			if s.Active() {
				f.Statuses = append(f.Statuses, s)
			}
		}
	case opts.Status != "":
		known := false
		for _, s := range core.Statuses {
			known = known || string(s) == opts.Status
		}
		if !known {
			return nil, invalidErr("status must be running, completed, failed or stopped")
		}
		f.Statuses = []core.Status{core.Status(opts.Status)}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	limit = min(limit, MaxListLimit)
	runs, err := m.Ops.Store.ListRuns(ctx, m.DB, f, limit)
	if err != nil {
		return nil, internalErr("failed to list the researches", err)
	}
	return runs, nil
}

// Counts returns how many runs of userID (of every user for "") each status has, sub-agents included; every status is in the map.
func (m *Manager) Counts(ctx context.Context, userID string) (map[core.Status]int, error) {
	out := make(map[core.Status]int, len(core.Statuses))
	for _, s := range core.Statuses {
		n, err := m.Ops.Store.CountRuns(ctx, m.DB, core.RunFilter{UserID: userID, Statuses: []core.Status{s}})
		if err != nil {
			return nil, internalErr("failed to count the researches", err)
		}
		out[s] = n
	}
	return out, nil
}

// ChildCounts returns, for each of the runs ids, how many sub-agents it spawned (a run with none is not in the map).
func (m *Manager) ChildCounts(ctx context.Context, ids []int64) (map[int64]int, error) {
	n, err := m.Ops.Store.ChildCounts(ctx, m.DB, ids)
	if err != nil {
		return nil, internalErr("failed to count the sub-agents", err)
	}
	return n, nil
}

// tx runs fn in a transaction.
func (m *Manager) tx(ctx context.Context, fn func(q db.Execer) error) error {
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
