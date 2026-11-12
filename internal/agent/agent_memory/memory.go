// Package agent_memory is how a research agent (package agent) reaches the internal knowledge base: the remember manager for its
// remember tool and the query pipeline for its query tool, in process, over the databases that were opened (SQLite or PostgreSQL, and
// the vector store). The job manager of the server (package agent_research) and the standalone binary (cmd/gligabao_agent) both
// use it. It does not import the job manager, the HTTP API, training or evals.
package agent_memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	gberrors "github.com/deepblue-phoenix/gligabao/internal/sys/errors"
	log "github.com/sirupsen/logrus"
)

// maxQueryFacts is how many of the facts a query is based on are given to the agent, after the answer.
const maxQueryFacts = 15

// Memory is how the research agents reach the internal knowledge base: the remember manager (which runs the remember pipeline, one
// run at a time) for the remember tool, and the query pipeline for the query tool. The agents get the tools only when they are given
// a Memory (Manager.UseMemory of the job manager; Knowledge for any other program). Everything is shared and safe for concurrent
// use; each agent gets its own view of it, for its user and dataset (see Knowledge). Make it once and do not copy it.
type Memory struct {
	// Remember is the remember manager; text is submitted to it as a request of the user, like POST /v1/gb/remember does.
	Remember *memory_remember.Manager
	// DB is the relational database, and LLM what answers the queries (the same as of the Remember manager).
	DB  *sql.DB
	LLM memory_core.LLM
	// RememberOpts and QueryOpts are the options of every remember and query call (vectors, chunking, geo, ...): those of the
	// remember and query endpoints.
	RememberOpts, QueryOpts []memory_core.Option
	// ErrSink stores the GBerrors of the calls that nobody waits for (a failed remember that did not wait: the text is lost); nil stores none.
	ErrSink gberrors.Sink

	// pending counts the remember calls that did not wait and are not over yet (see Drain).
	pending sync.WaitGroup
}

// Knowledge returns the knowledge base of one agent: the dataset of the user, with sessionID on everything it remembers and queries.
// ctx is the lifetime of the agent's remember calls that do not wait: it ends when the process stops, not when the tool returns.
func (m *Memory) Knowledge(ctx context.Context, userID, dataset, sessionID string) agent.Knowledge {
	return &knowledge{mem: m, userID: userID, dataset: dataset, sessionID: sessionID, ctx: ctx}
}

// Drain waits until every remember call that did not wait (the default of the tool) is over, or ctx is done. A program that stops
// after the agent ended (a one-shot command) must call it before it cancels the context of the remember manager and closes the
// databases, or the text still queued is lost. Call it when no agent is working any more.
func (m *Memory) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		m.pending.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SessionPrefix starts the session ID of the remember and query calls of an agent; SessionID is the whole of it.
const SessionPrefix = "research-"

// SessionID is the session of the calls the agent of run runID makes: everything it remembers (the artifacts, entities and facts) and
// every query it asks carries it, so that what an agent contributed can be told (see the provenance of the admin UI).
func SessionID(runID int64) string { return fmt.Sprintf("%s%d", SessionPrefix, runID) }

// knowledge is the Knowledge of one research run: the knowledge base of the user, in one dataset. Remembered text and queries carry
// the session "research-<run id>", so that what a run stored and asked can be told in the run logs.
type knowledge struct {
	mem       *Memory
	userID    string
	dataset   string
	sessionID string
	// ctx is the lifetime of the run's remember calls that do not wait: it ends when the server stops, not when the tool returns.
	ctx context.Context
}

var _ agent.Knowledge = (*knowledge)(nil)

// Remember submits text to the remember manager. With wait it blocks for the outcome of the run, and returns what was stored;
// without it, it returns when the text is queued, with the ID of the run (gb_remember_runs) that holds how it goes.
func (k *knowledge) Remember(ctx context.Context, text string, wait bool) (string, error) {
	req := memory_remember.Request{
		UserID: k.userID, Dataset: k.dataset, Text: text,
		Opts: append(k.mem.RememberOpts[:len(k.mem.RememberOpts):len(k.mem.RememberOpts)], memory_core.WithSessionID(k.sessionID)),
	}
	if !wait {
		// Nobody waits for the run: it must not end with the tool call, only with the server.
		outcome := make(chan memory_remember.Outcome, 1)
		req.Ctx, req.Outcome = k.ctx, outcome
		k.mem.pending.Add(1)
		runID, err := k.mem.Remember.Submit(req)
		if err != nil {
			k.mem.pending.Done()
			return "", fmt.Errorf("could not queue the text: %w", err)
		}
		go func() { // the manager always sends the outcome, also for a run that was cancelled or never began
			defer k.mem.pending.Done()
			if out := <-outcome; out.Err != nil {
				log.WithError(out.Err).WithField("remember_run_id", runID).Warn("agent memory: a remember call that did not wait failed")
				// Nobody is told about this text (the agent has moved on): it is lost, so it is tracked.
				gberrors.New(context.WithoutCancel(k.ctx), k.mem.ErrSink, "agent.remember.async", out.Err,
					gberrors.Meta{"remember_run_id": runID, "user_id": k.userID, "dataset": k.dataset, "session_id": k.sessionID})
			}
		}()
		return fmt.Sprintf("queued (remember run %d); it is being absorbed into the knowledge base in the background, and may not be found by query yet", runID), nil
	}
	outcome := make(chan memory_remember.Outcome, 1)
	req.Ctx, req.Outcome = ctx, outcome
	runID, err := k.mem.Remember.Submit(req)
	if err != nil {
		return "", fmt.Errorf("could not queue the text: %w", err)
	}
	select {
	case out := <-outcome:
		if out.Err != nil {
			return "", fmt.Errorf("remember run %d failed: %w", runID, out.Err)
		}
		return fmt.Sprintf("remembered (remember run %d): %d entities and %d facts in the knowledge base", runID, len(out.Result.Entities), len(out.Result.Edges)), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Query asks the knowledge base: the answer, then the facts it is based on. A dataset that does not exist yet (nothing was ever
// remembered into it) is not an error, the knowledge base is just empty.
func (k *knowledge) Query(ctx context.Context, question string) (string, error) {
	res, err := memory_query.Query(ctx, k.mem.DB, k.mem.LLM, k.userID, k.dataset, question,
		append(k.mem.QueryOpts[:len(k.mem.QueryOpts):len(k.mem.QueryOpts)], memory_core.WithSessionID(k.sessionID))...)
	if errors.Is(err, memory_core.ErrDatasetNotFound) {
		return "The knowledge base is empty: nothing was remembered yet.", nil
	}
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(res.Answer))
	if n := len(res.Evidence.Edges); n > 0 {
		sb.WriteString("\n\nFacts it is based on:")
		for i, e := range res.Evidence.Edges {
			if i == maxQueryFacts {
				fmt.Fprintf(&sb, "\n- ... and %d more", n-i)
				break
			}
			fmt.Fprintf(&sb, "\n- %s %s %s", e.Source, e.Relation, e.Target)
			if e.Description != "" {
				fmt.Fprintf(&sb, ": %s", e.Description)
			}
		}
	}
	return sb.String(), nil
}
