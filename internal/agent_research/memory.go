package agent_research

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_query"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
)

// maxQueryFacts is how many of the facts a query is based on are given to the agent, after the answer.
const maxQueryFacts = 15

// Memory is how the research agents reach the internal knowledge base: the remember manager (which runs the remember pipeline, one
// run at a time) for the remember tool, and the query pipeline for the query tool. The agents get the tools only when the Manager has
// a Memory (UseMemory). Everything is shared and safe for concurrent use; each agent gets its own view of it, for its user and
// dataset (see knowledge).
type Memory struct {
	// Remember is the remember manager; text is submitted to it as a request of the user, like POST /v1/gb/remember does.
	Remember *memory_remember.Manager
	// DB is the relational database, and LLM what answers the queries (the same as of the Remember manager).
	DB  *sql.DB
	LLM memory_core.LLM
	// RememberOpts and QueryOpts are the options of every remember and query call (vectors, chunking, geo, ...): those of the
	// remember and query endpoints.
	RememberOpts, QueryOpts []memory_core.Option
}

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
		req.Ctx = k.ctx
		runID, err := k.mem.Remember.Submit(req)
		if err != nil {
			return "", fmt.Errorf("could not queue the text: %w", err)
		}
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
