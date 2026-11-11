package investigate

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate_core"
)

// The fan-out of the loop (docs/agents/subagents.md): when several questions of the frontier can be worked on independently, the loop hands
// them to workers, waits for all of them and reduces their findings into the frontier and the notes. No model call of the supervisor is
// made for it: the code decides, and only the supervisor writes the state.

// WorkerRequest is what a worker is started with: the question and its brief (investigate_core.WorkerBrief).
type WorkerRequest struct {
	QuestionID string
	Question   string
	Brief      string
	// MaxCostUSD is the most the worker should spend (0: no cap of the run's): see investigate_core.WorkerCostShare.
	MaxCostUSD float64
}

// WorkerResult is what a worker hands back, and what it spent (also when it failed).
type WorkerResult struct {
	Findings                  investigate_core.Findings
	InputTokens, OutputTokens int
	CostUSD                   float64
}

// Workers runs workers. Run blocks until the worker has ended, and may be called from several goroutines at once. An error is a worker that
// failed (the question goes back to the frontier, the run goes on); a cancelled ctx stops the worker.
type Workers interface {
	Run(ctx context.Context, req WorkerRequest) (WorkerResult, error)
}

// Delegation says how many workers the loop may use and how the briefs are made; the zero value (or no Workers) is no delegation.
type Delegation struct {
	Workers Workers
	// MaxParallel is the most workers at once (below 2: no delegation); MaxWorkers the most in the whole run (0: no limit);
	// MinPriority the priority below which a question is not delegated; ContextTokens the size of the slice of the notes in a brief.
	MaxParallel, MaxWorkers int
	MinPriority             float64
	ContextTokens           int
}

func (d Delegation) on() bool { return d.Workers != nil && d.MaxParallel > 1 }

// delegated is one question with its worker's outcome.
type delegated struct {
	req WorkerRequest
	res WorkerResult
	err error
}

// fanOut hands the questions that can go to workers, if there are two or more, and reduces what they bring back. moved says that the
// frontier changed and the iteration is over; false means that the loop works on a question itself, as without workers.
func (r *runner) fanOut(ctx context.Context, st investigate_core.State) (bool, error) {
	d := r.cfg.Delegation
	room := d.MaxParallel
	if d.MaxWorkers > 0 {
		room = min(room, d.MaxWorkers-r.workers)
	}
	if room < 2 || !investigate_core.DelegateBudgetOK(r.ls, st.Limits) {
		return false, nil
	}
	batch := investigate_core.SelectBatch(st.Frontier, room, d.MinPriority)
	if len(batch) < 2 {
		return false, nil
	}
	ids := make([]string, len(batch))
	reqs := make([]WorkerRequest, len(batch))
	for i, q := range batch {
		ids[i] = q.ID
		reqs[i] = WorkerRequest{QuestionID: q.ID, Question: q.Question, Brief: investigate_core.WorkerBrief(st, q.ID, d.ContextTokens),
			MaxCostUSD: investigate_core.WorkerCostShare(r.ls, st.Limits, len(batch))}
	}
	if _, _, err := investigate_core.Delegate(st.Frontier, ids); err != nil {
		return false, nil // not what the selection said (cannot happen with the state just loaded): work on it serially
	}
	turn := r.ls.Turns + 1
	r.entered(turn, investigate_core.StateDelegating, strings.Join(ids, ", "))
	if err := r.sess.Apply(ctx, "", func(s investigate_core.State) (investigate_core.State, []investigate_core.Transition) {
		f, ts, err := investigate_core.Delegate(s.Frontier, ids)
		if err != nil {
			return s, nil
		}
		s.Frontier = f
		return s, ts
	}); err != nil {
		return false, err
	}
	r.workers += len(reqs)
	results := runWorkers(ctx, d.Workers, reqs)
	r.entered(turn, investigate_core.StateWaiting, strings.Join(ids, ", "))
	for _, w := range results { // what they spent counts for the breakers, not for the totals of the run's own row
		r.ls = r.ls.Spend(w.res.InputTokens, w.res.OutputTokens, w.res.CostUSD)
		r.workerCost += w.res.CostUSD
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	for _, w := range results { // in question order: the same findings give the same state whatever the order they came in
		if err := r.reduce(ctx, w); err != nil {
			return true, err
		}
	}
	return true, nil
}

// runWorkers runs one worker for each request at the same time and waits for all of them; the results are in the order of the requests.
func runWorkers(ctx context.Context, w Workers, reqs []WorkerRequest) []delegated {
	out := make([]delegated, len(reqs))
	var wg sync.WaitGroup
	for i, req := range reqs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					out[i] = delegated{req: req, err: fmt.Errorf("the worker panicked: %v", p)}
				}
			}()
			res, err := w.Run(ctx, req)
			out[i] = delegated{req: req, res: res, err: err}
		}()
	}
	wg.Wait()
	return out
}

// reduce applies the outcome of one worker to the state.
func (r *runner) reduce(ctx context.Context, w delegated) error {
	return r.sess.Apply(ctx, "worker", func(s investigate_core.State) (investigate_core.State, []investigate_core.Transition) {
		if w.err != nil {
			next, ts, _ := investigate_core.WorkerFailed(s, w.req.QuestionID, w.err.Error())
			return next, ts
		}
		next, ts, _ := investigate_core.Reduce(s, w.req.QuestionID, w.res.Findings)
		return next, ts
	})
}

func (r *runner) entered(turn int, state, detail string) {
	if h := r.cfg.Hooks.Entered; h != nil {
		h(turn, state, detail)
	}
}
