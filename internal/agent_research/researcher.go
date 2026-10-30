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
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
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

	// Its own.
	run   *core.Run // the record of the run, as created
	opts  Options
	agent *agent.Agent
	http  *http.Client // reads the pages of this agent only
	cur   *core.State  // the state the run is in
	total agent.Result // the totals of the agent so far
}

// newResearcher makes the researcher of run (already recorded) with its own agent, observed by the researcher itself. mem is the
// knowledge base the agent works with, in the dataset of opts (nil: none).
func newResearcher(m *Manager, run *core.Run, opts Options, mem *Memory) *researcher {
	r := &researcher{
		ctx:    m.ctx,
		dbCtx:  context.WithoutCancel(m.ctx),
		db:     m.DB,
		store:  m.Ops.Store,
		logf:   log.WithFields(log.Fields{"run_id": run.ID, "user_id": run.UserID}),
		client: m.Client,
		run:    run,
		opts:   opts,
		http:   agent.NewHTTPClient(),
	}
	var kb agent.Knowledge // a nil Memory must become a nil Knowledge
	if mem != nil {
		kb = &knowledge{mem: mem, userID: run.UserID, dataset: opts.Dataset, sessionID: fmt.Sprintf("research-%d", run.ID), ctx: r.ctx}
	}
	r.agent = agent.ResearchAgent(r.client, r.http, kb)
	r.agent.Model, r.agent.MaxSteps = opts.Model, opts.MaxSteps
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
	defer r.http.CloseIdleConnections()
	r.logf.WithField("topic", truncate(r.run.Topic, 100)).Info("agent research: started")
	defer func() {
		if p := recover(); p != nil {
			r.logf.Errorf("agent research: the agent panicked: %v", p)
			r.finish(core.StatusFailed, "", fmt.Sprintf("internal error: %v", p))
		}
	}()

	res, err := r.agent.Run(r.ctx, r.opts.Topic)
	if res != nil {
		r.total = *res
	}
	switch {
	case r.ctx.Err() != nil:
		r.finish(core.StatusFailed, "", "interrupted: the server stopped while the agent worked")
	case err != nil:
		r.logf.WithError(err).Warn("agent research: the agent failed")
		r.finish(core.StatusFailed, "", truncate(err.Error(), maxTextLen))
	case strings.TrimSpace(res.Answer) == "":
		r.finish(core.StatusFailed, "", "the agent ended without a report")
	default:
		r.finish(core.StatusCompleted, res.Answer, "")
	}
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
		r.enter(core.StateWritingReport, step, fmt.Sprintf("step %d of %d, the last: no tools, the answer is the report", step, r.opts.MaxSteps))
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
	r.total = t
	if err := r.store.UpdateUsage(r.dbCtx, r.db, r.run.ID, usageOf(t)); err != nil {
		r.logf.WithError(err).Warn("agent research: recording the usage failed")
	}
}

// finish ends the run's record: the final usage, the status, the report or the error, and the terminal state.
func (r *researcher) finish(status core.Status, report, errText string) {
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
