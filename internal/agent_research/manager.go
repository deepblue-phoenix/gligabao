// Package agent_research runs the deep-research agent (package agent) as a job: a Manager starts a research on a topic in the
// background, records the run in the database (gb_agent_research_runs) and every state the agent goes through while it works
// (gb_agent_research_states: searching, reading, thinking, ...), so that its progress can be followed in detail, and lists and reads
// the runs. The HTTP handlers are in internal/api/research, the MCP tools in internal/api/mcp and the commands in package cli.
package agent_research

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
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
	// MaxActivePerUser is how many agents of one user may work at once (DefaultMaxActivePerUser if 0).
	MaxActivePerUser int

	ctx context.Context

	mu     sync.Mutex
	active map[string]int // running agents by user
	wg     sync.WaitGroup
}

// NewManager makes the manager of the research agents whose runs are kept in d. client runs the agents (may be nil: Start then fails
// with KindUnavailable). The agents stop when ctx is done. Runs that a restart interrupted (still running in the database) are marked
// failed.
func NewManager(ctx context.Context, d *sql.DB, ops Ops, client *ai.Client) *Manager {
	m := &Manager{DB: d, Client: client, Ops: ops, ctx: ctx, active: map[string]int{}}
	if k, err := ops.Store.FailActive(ctx, d, "interrupted: the server stopped while the agent worked"); err != nil {
		log.WithError(err).Warn("agent research: could not mark interrupted runs as failed")
	} else if k > 0 {
		log.Infof("agent research: %d run(s) were interrupted by a restart and marked failed", k)
	}
	return m
}

// Wait blocks until the running agents have ended (after the manager's context is done, or for tests).
func (m *Manager) Wait() { m.wg.Wait() }

// Start records a research of opts.Topic for userID (already validated), starts the agent in the background and returns the recorded
// run (status running, state starting). Errors are *Error: KindInvalid for bad options, KindUnavailable without an LLM, KindConflict
// if the user has too many agents working, KindInternal.
func (m *Manager) Start(ctx context.Context, userID string, opts Options) (*core.Run, error) {
	if m.Client == nil {
		return nil, unavailableErr("no LLM is available for the research agent (set ANTHROPIC_API_KEY)")
	}
	opts, err := opts.resolve()
	if err != nil {
		return nil, err
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
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		if m.active[userID]--; m.active[userID] <= 0 {
			delete(m.active, userID)
		}
		m.mu.Unlock()
	}

	run := &core.Run{UserID: userID, Topic: opts.Topic, Status: core.StatusRunning, State: core.StateStarting, Model: opts.Model, MaxSteps: opts.MaxSteps}
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
	rec := *run // what the caller gets, before the goroutine works on run

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer release()
		m.run(run, opts)
	}()
	return &rec, nil
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
	f := core.RunFilter{UserID: userID}
	switch {
	case opts.Active && opts.Status != "":
		return nil, invalidErr("give active or status, not both")
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
			return nil, invalidErr("status must be running, completed or failed")
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

// run is the goroutine of an agent: it runs the agent, records every state it enters and what it spends, and ends the run's record.
// A failed write of the progress is logged and does not stop the agent: the research is worth more than its trace.
func (m *Manager) run(run *core.Run, opts Options) {
	dbCtx := context.WithoutCancel(m.ctx) // the writes must outlive the cancelled agent
	logf := log.WithFields(log.Fields{"run_id": run.ID, "user_id": run.UserID})
	logf.WithField("topic", truncate(run.Topic, 100)).Info("agent research: started")

	finish := func(status core.Status, report, errText string, total agent.Result) {
		err := m.tx(dbCtx, func(q db.Execer) error {
			if err := m.Ops.Store.UpdateUsage(dbCtx, q, run.ID, usageOf(total)); err != nil {
				return err
			}
			_, err := m.Ops.Store.FinishRun(dbCtx, q, run.ID, status, report, errText)
			return err
		})
		if err != nil {
			logf.WithError(err).Error("agent research: ending the run failed")
		}
		logf.WithField("status", status).Info("agent research: ended")
	}
	var total agent.Result
	defer func() {
		if r := recover(); r != nil {
			logf.Errorf("agent research: the agent panicked: %v", r)
			finish(core.StatusFailed, "", fmt.Sprintf("internal error: %v", r), total)
		}
	}()

	var cur *core.State // the state the run is in
	enter := func(state string, step int, detail string) {
		detail = truncate(detail, maxTextLen)
		err := m.tx(dbCtx, func(q db.Execer) (err error) {
			cur, err = m.Ops.Store.EnterState(dbCtx, q, run.ID, state, step, detail)
			return err
		})
		if err != nil {
			logf.WithError(err).WithField("state", state).Warn("agent research: recording the state failed")
		}
	}
	saveUsage := func(t agent.Result) {
		total = t
		if err := m.Ops.Store.UpdateUsage(dbCtx, m.DB, run.ID, usageOf(t)); err != nil {
			logf.WithError(err).Warn("agent research: recording the usage failed")
		}
	}

	a := agent.ResearchAgent(m.Client)
	a.Model, a.MaxSteps = opts.Model, opts.MaxSteps
	a.Observer = agent.Observer{
		BeforeModel: func(step int, final bool) {
			if final {
				enter(core.StateWritingReport, step, fmt.Sprintf("step %d of %d, the last: no tools, the answer is the report", step, opts.MaxSteps))
				return
			}
			enter(core.StateThinking, step, fmt.Sprintf("step %d of %d", step, opts.MaxSteps))
		},
		AfterModel: func(_ int, t agent.Result) { saveUsage(t) },
		BeforeTool: func(step int, name string, input json.RawMessage) {
			state, detail := toolState(name, input)
			enter(state, step, detail)
		},
		AfterTool: func(_ int, _, output string, isErr bool, t agent.Result) {
			errText := ""
			if isErr {
				errText = truncate(output, maxTextLen)
			}
			if cur != nil {
				if err := m.Ops.Store.CloseState(dbCtx, m.DB, cur.ID, errText); err != nil {
					logf.WithError(err).Warn("agent research: ending the state failed")
				}
			}
			saveUsage(t)
		},
	}
	res, err := a.Run(m.ctx, opts.Topic)
	if res != nil {
		total = *res
	}
	switch {
	case m.ctx.Err() != nil:
		finish(core.StatusFailed, "", "interrupted: the server stopped while the agent worked", total)
	case err != nil:
		logf.WithError(err).Warn("agent research: the agent failed")
		finish(core.StatusFailed, "", truncate(err.Error(), maxTextLen), total)
	case strings.TrimSpace(res.Answer) == "":
		finish(core.StatusFailed, "", "the agent ended without a report", total)
	default:
		finish(core.StatusCompleted, res.Answer, "", total)
	}
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
