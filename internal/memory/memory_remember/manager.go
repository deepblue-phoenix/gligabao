package memory_remember

// The remember manager: one goroutine that runs every remember pipeline run, one at a time. See docs/remember-manager.md.
//
// Whoever wants text remembered (the HTTP handlers, anything else) does not call Remember: it sends a Request to the
// manager with Submit. Submit queues it and returns at once with the ID of the run (its row in gb_remember_runs), which
// is how the caller checks the run later (Status, or the run log in the admin UI). The manager takes the requests off
// the queue in order and runs them one by one (for now runs are serialized).
//
// A caller that wants the result puts a channel in the request (Request.Outcome, capacity 1, made by the caller): the
// manager sends it the Outcome of the run, what Remember returned, when the run is over. The handlers block on it.
// Without one the request is fire and forget: look at the run log.
//
// The queue itself is two operations, enqueue and dequeue (below), that carry run IDs: by default a Go channel, or the
// callbacks of WithQueue, so that an external queue system can be used.
//
// Until the manager takes a request up, its run is "queued": its row is in status started with every stage not started
// (the schema has no status of its own for it). The row is overwritten when the run begins (see startRunLog).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"

	log "github.com/sirupsen/logrus"
)

// Request asks the manager for one run of Remember. See Remember for the fields.
type Request struct {
	UserID, Dataset, Kind, Text string
	Ref                         time.Time
	Opts                        []memory_core.Option

	// Ctx is the context of the run; it is what cancels it (nil means context.Background()). A request whose context is
	// done when its turn comes fails without running. A caller that does not wait for the run (no Outcome) should pass a
	// context that is not cancelled when it returns (context.WithoutCancel).
	Ctx context.Context

	// Outcome (optional) is where the manager sends the Outcome of the run when it is over (it never blocks on it): the
	// caller makes the channel and receives from it to wait for the run. It must have a capacity of at least 1; Submit
	// refuses it otherwise.
	Outcome chan<- Outcome
}

// Outcome is what a run of Remember returned.
type Outcome struct {
	Result *Result
	Err    error
}

// QueueHooks are the two operations of the queue of the manager. The default is a Go channel; an embedder that has a queue
// system of its own (Redis, SQS, ...) supplies these callbacks instead (WithQueue). The queue carries only the ID of the run
// (its row in gb_remember_runs), not the request: the request (its context, options and channels cannot be serialized)
// stays in the memory of the process, so the process that enqueues an ID is the one that has to dequeue it. A dequeued ID that
// is not a pending request of this process (e.g. left over from an earlier one) is logged and skipped.
type QueueHooks struct {
	// Enqueue adds the run to the queue. It may block (a full queue) but must return when ctx is done. If it fails the
	// request is not queued and Submit returns the error.
	Enqueue func(ctx context.Context, runID int64) error
	// Dequeue blocks until there is a run to take, and returns its ID; it must return (with an error) when ctx is done. An
	// error while ctx is not done is logged and Dequeue is tried again after a second.
	Dequeue func(ctx context.Context) (runID int64, err error)
}

// Manager is the goroutine that runs the remember pipeline. See the top of this file.
type Manager struct {
	d   *sql.DB
	llm memory_core.LLM
	ctx context.Context

	queue QueueHooks

	mu      sync.Mutex        // guards pending and closed
	pending map[int64]*queued // the requests that are queued, by run ID
	closed  bool
	done    chan struct{} // closed when the goroutine has returned
}

type queued struct {
	Request
	ctx   context.Context
	runID int64
}

// ErrManagerClosed is returned by Submit (and recorded as the error of the requests still queued) once the context of the
// manager is done.
var ErrManagerClosed = errors.New("memory: remember manager is closed")

// queueSize is how many run IDs wait in the default queue before Submit blocks.
const queueSize = 256

// OK says the run succeeded.
func (o Outcome) OK() bool { return o.Err == nil }

// chanQueue is the default queue: a Go channel.
func chanQueue() QueueHooks {
	ch := make(chan int64, queueSize)
	return QueueHooks{
		Enqueue: func(ctx context.Context, id int64) error {
			select {
			case ch <- id:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		Dequeue: func(ctx context.Context) (int64, error) {
			select {
			case id := <-ch:
				return id, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		},
	}
}

// ManagerOption customizes NewManager.
type ManagerOption func(*Manager)

// WithQueue makes the manager use the queue operations of h instead of a Go channel. Both callbacks are required.
func WithQueue(h QueueHooks) ManagerOption {
	return func(m *Manager) {
		if h.Enqueue == nil || h.Dequeue == nil {
			panic("memory_remember: WithQueue needs both Enqueue and Dequeue")
		}
		m.queue = h
	}
}

// NewManager starts the manager's goroutine. d and llm are what every run uses (as the arguments of Remember). It runs
// until ctx is done: the run in progress is cancelled, the requests still queued fail with ErrManagerClosed, and Submit
// returns ErrManagerClosed from then on.
func NewManager(ctx context.Context, d *sql.DB, llm memory_core.LLM, opts ...ManagerOption) *Manager {
	m := &Manager{d: d, llm: llm, ctx: ctx, queue: chanQueue(), pending: map[int64]*queued{}, done: make(chan struct{})}
	for _, o := range opts {
		o(m)
	}
	go m.loop()
	return m
}

// Wait blocks until the goroutine has returned, which is after the context of the manager is done.
func (m *Manager) Wait() { <-m.done }

// Submit queues the request and returns the ID of its run (its row in gb_remember_runs; see Status) as soon as it is
// queued, without waiting for the run. It returns an error, and queues nothing, if the identity or kind of the request
// is invalid, the row cannot be created, the queue refuses it (or ctx, or the request's, is done while the queue is
// full), or the manager is closed. Whatever else is wrong with the request is the run's error.
func (m *Manager) Submit(req Request) (runID int64, err error) {
	if req.Outcome != nil && cap(req.Outcome) < 1 {
		return 0, errors.New("memory: the outcome channel of a remember request needs a capacity of at least 1")
	}
	userID, err := memory_core.ResolveUserID(req.UserID)
	if err != nil {
		return 0, err
	}
	dataset, err := memory_core.ResolveDatasetName(req.Dataset)
	if err != nil {
		return 0, err
	}
	kind, err := ResolveKind(req.Kind)
	if err != nil {
		return 0, err
	}
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0, ErrManagerClosed
	}
	m.mu.Unlock()

	run := relational.NewRememberRun(userID, dataset, kind, map[string]any{"queued": true}, relational.Now())
	runID, err = relational.CreateRememberRun(context.WithoutCancel(ctx), m.d, run)
	if err != nil {
		return 0, fmt.Errorf("memory: create remember run: %w", err)
	}
	q := &queued{Request: req, ctx: ctx, runID: runID}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.fail(runID, ErrManagerClosed)
		return 0, ErrManagerClosed
	}
	m.pending[runID] = q // before it is in the queue, so that the loop finds it as soon as it is
	m.mu.Unlock()

	// Enqueueing ends when the request's context or the manager's does.
	eCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(m.ctx, cancel)()
	if err := m.enqueue(eCtx, runID); err != nil {
		m.mu.Lock()
		_, ours := m.pending[runID]
		delete(m.pending, runID)
		m.mu.Unlock()
		if !ours { // the manager closed meanwhile and has failed the request already
			return 0, ErrManagerClosed
		}
		if m.ctx.Err() != nil {
			err = ErrManagerClosed
		}
		m.fail(runID, err)
		return 0, err
	}
	return runID, nil
}

// Status returns the run log of the run: its status, stages and error (relational.ErrNotFound if there is no such run).
func (m *Manager) Status(ctx context.Context, runID int64) (*relational.RememberRun, error) {
	return relational.GetRememberRun(ctx, m.d, runID)
}

//---------------------------------------------------------------------------------------------
// The queue

// enqueue puts the run in the queue.
func (m *Manager) enqueue(ctx context.Context, runID int64) error {
	return m.queue.Enqueue(ctx, runID)
}

// dequeue takes the next run out of the queue (blocking), and returns its request. It returns nil request and nil error for
// an ID that is not a pending request of this process.
func (m *Manager) dequeue(ctx context.Context) (*queued, error) {
	id, err := m.queue.Dequeue(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	q := m.pending[id]
	delete(m.pending, id)
	m.mu.Unlock()
	if q == nil {
		log.WithField("remember_run_id", id).Warn("remember manager: dequeued a run that is not pending in this process; skipped")
	}
	return q, nil
}

//---------------------------------------------------------------------------------------------

func (m *Manager) loop() {
	defer close(m.done)
	for {
		q, err := m.dequeue(m.ctx)
		switch {
		case m.ctx.Err() != nil:
			if q != nil { // dequeued as the manager closed
				m.finish(q, nil, ErrManagerClosed)
			}
			m.shutdown()
			return
		case err != nil:
			log.WithError(err).Error("remember manager: dequeue")
			select {
			case <-time.After(time.Second):
			case <-m.ctx.Done():
			}
		case q != nil:
			m.run(q)
		}
	}
}

// shutdown closes the manager: nothing is queued from now on, and the requests that wait fail.
func (m *Manager) shutdown() {
	m.mu.Lock()
	m.closed = true
	waiting := m.pending
	m.pending = map[int64]*queued{}
	m.mu.Unlock()
	for _, q := range waiting {
		m.finish(q, nil, ErrManagerClosed)
	}
}

// run runs one request, and never lets it take the goroutine down.
func (m *Manager) run(q *queued) {
	var res *Result
	var err error
	defer func() {
		if p := recover(); p != nil {
			log.WithFields(log.Fields{"remember_run_id": q.runID, "panic": p, "stack": string(debug.Stack())}).Error("remember run panicked")
			res, err = nil, fmt.Errorf("memory: remember panicked: %v", p)
		}
		m.finish(q, res, err)
	}()
	if err = q.ctx.Err(); err != nil { // cancelled while it waited
		return
	}
	// The run ends with the request's context or the manager's, whichever is first.
	ctx, cancel := context.WithCancel(q.ctx)
	defer cancel()
	defer context.AfterFunc(m.ctx, cancel)()
	opts := append(q.Opts[:len(q.Opts):len(q.Opts)], memory_core.WithRememberRunID(q.runID))
	res, err = Remember(ctx, m.d, m.llm, q.UserID, q.Dataset, q.Kind, q.Text, q.Ref, opts...)
}

// finish closes the request: its run log (if the run did not), and its outcome channel.
func (m *Manager) finish(q *queued, res *Result, err error) {
	if err != nil {
		m.fail(q.runID, err)
	}
	if q.Outcome != nil {
		select {
		case q.Outcome <- Outcome{Result: res, Err: err}:
		default: // a full channel: the caller broke the contract; never block the manager on it
			log.WithField("remember_run_id", q.runID).Error("remember manager: outcome channel is full")
		}
	}
}

// fail marks the run failed with err, unless it already ended (Remember does that itself for the errors it returns; this is
// for a run that never began, was cancelled in the queue, or panicked).
func (m *Manager) fail(runID int64, err error) {
	ctx := context.Background()
	run, getErr := relational.GetRememberRun(ctx, m.d, runID)
	if getErr != nil {
		log.WithError(getErr).WithField("remember_run_id", runID).Error("remember manager: load run")
		return
	}
	if run.Status != relational.RememberStarted {
		return
	}
	l := &runLog{runState: &runState{d: m.d, ctx: ctx, run: *run}}
	l.Finish(err)
}
