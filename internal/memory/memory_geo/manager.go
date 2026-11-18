package memory_geo

// The geo manager: ONE goroutine per gligabao server that owns the geo resolver and asks it for one place at a time, so that a slow
// or rate-limited resolver (the public Nominatim allows one request a second) is never asked by two callers at once and no pipeline
// has to wait for it.
//
// It has two queues, and the query queue always wins:
//
//   - the QUERY queue is what Manager.Resolve (the Resolver interface of the manager) puts a request into. A query pipeline blocks on
//     the answer, because the answer is part of what it returns. The manager takes from this queue first, and only looks at the
//     remember queue when the query queue is empty (a request that is already being resolved is not interrupted).
//   - the REMEMBER queue is what Manager.Enqueue puts a Job into. The caller does not wait: the job carries the function (Done) that
//     the manager calls with the answer, in its own goroutine, which is what stores the coordinates. Jobs are keyed (the key is the
//     ID of the entity), and a key that is already queued is not queued again. Flush waits until the jobs queued before it are done.
//
// The manager has a context of its own (the one given to NewManager, not the one of any request): it lives until that is cancelled.
// Both queues are bounded; a full one refuses new work (ErrQueueFull) instead of blocking the pipeline.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Job is one place that remember wants resolved, without waiting for the answer.
type Job struct {
	// Key identifies the job (the ID of the entity, always > 0): a job whose key is queued or being resolved is refused (ErrDuplicate).
	Key int64
	// Query is what to ask the resolver.
	Query Query
	// Done is called, from the goroutine of the manager, with the answer (a place, ErrNotFound, or the failure of the resolver). It is
	// not called if the manager stopped before the job was resolved. It must not block for long: it delays the next job.
	Done func(ctx context.Context,
		o Outcome)

	// barrier is a job of Flush: a channel to close when every job before it is done.
	barrier chan struct{}
}

// Outcome is the answer to a Job.
type Outcome struct {
	// Place is what the resolver found; nil if Err is not nil.
	Place *Place
	// Err is ErrNotFound if the resolver knows no place for the query, or the failure of the resolver (see Resolver.Resolve).
	Err error
	// Started and Latency are when the manager asked the resolver, and how long it took.
	Started time.Time
	Latency time.Duration
}

// Queue is the part of the Manager that remember uses.
type Queue interface {
	// Enqueue adds the job to the remember queue and returns at once: ErrDuplicate if the key is already queued or being resolved,
	// ErrQueueFull if the queue is full, ErrStopped if the manager stopped.
	Enqueue(j Job) error
	// Flush returns when every job enqueued before it is done (its Done has returned), or with the error of ctx, or ErrStopped.
	Flush(ctx context.Context) error
}

var (
	// ErrQueueFull means a queue of the manager is full.
	ErrQueueFull = errors.New("geo: the queue is full")
	// ErrDuplicate means a job with the same key is already queued or being resolved.
	ErrDuplicate = errors.New("geo: the place is already queued")
	// ErrStopped means the manager stopped (its context was cancelled).
	ErrStopped = errors.New("geo: the manager stopped")
)

const (
	// DefaultQueryQueue and DefaultRememberQueue are the depths of the two queues.
	DefaultQueryQueue    = 1000
	DefaultRememberQueue = 10000
	// DefaultResolveTimeout bounds each call of the resolver.
	DefaultResolveTimeout = 30 * time.Second
)

// ManagerOption customizes NewManager.
type ManagerOption func(*managerConfig)

type managerConfig struct {
	queryQueue, rememberQueue int
	timeout                   time.Duration
}

// WithQueueDepths sets the depths of the query queue and of the remember queue (values below 1 keep the default).
func WithQueueDepths(query,
	remember int) ManagerOption {
	return func(c *managerConfig) {
		if query > 0 {
			c.queryQueue = query
		}
		if remember > 0 {
			c.rememberQueue = remember
		}
	}
}

// WithResolveTimeout bounds each call of the resolver (values of 0 or less keep the default).
func WithResolveTimeout(d time.Duration) ManagerOption {
	return func(c *managerConfig) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// Manager is the geo manager. It is a Resolver (the query queue) and a Queue (the remember queue).
type Manager struct {
	resolver Resolver
	ctx      context.Context
	timeout  time.Duration

	queryQ    chan queryRequest
	rememberQ chan Job
	done      chan struct{}

	mu      sync.Mutex
	pending map[int64]struct{}
}

type queryRequest struct {
	ctx   context.Context
	q     Query
	reply chan queryReply
}

type queryReply struct {
	place *Place
	err   error
}

// NewManager starts the goroutine of the manager, which asks r for every place and runs until ctx is cancelled (see Wait).
func NewManager(ctx context.Context,
	r Resolver,
	opts ...ManagerOption) *Manager {
	cfg := managerConfig{
		queryQueue:    DefaultQueryQueue,
		rememberQueue: DefaultRememberQueue,
		timeout:       DefaultResolveTimeout,
	}
	for _, o := range opts {
		o(&cfg)
	}
	m := &Manager{
		resolver:  r,
		ctx:       ctx,
		timeout:   cfg.timeout,
		queryQ:    make(chan queryRequest, cfg.queryQueue),
		rememberQ: make(chan Job, cfg.rememberQueue),
		done:      make(chan struct{}),
		pending:   map[int64]struct{}{},
	}
	go m.loop()
	return m
}

// Wait blocks until the goroutine of the manager has stopped (after its context was cancelled).
func (m *Manager) Wait() { <-m.done }

// Name is the name of the resolver the manager asks.
func (m *Manager) Name() string { return m.resolver.Name() }

// Pending is how many keys are queued or being resolved.
func (m *Manager) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

// Resolve is the query queue: it puts the request into it and waits for the answer. The manager serves it before any remember job.
// It returns when ctx is done even if the manager has not got to the request yet (which is then skipped).
func (m *Manager) Resolve(ctx context.Context,
	q Query) (*Place, error) {
	req := queryRequest{
		ctx:   ctx,
		q:     q,
		reply: make(chan queryReply, 1),
	}
	select {
	case m.queryQ <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.ctx.Done():
		return nil, ErrStopped
	}
	select {
	case r := <-req.reply:
		return r.place, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.ctx.Done():
		return nil, ErrStopped
	}
}

// Enqueue is the remember queue (see Queue).
func (m *Manager) Enqueue(j Job) error {
	if j.Key <= 0 || j.Done == nil {
		return errors.New("geo: a job needs a key and a Done function")
	}
	if m.ctx.Err() != nil {
		return ErrStopped
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pending[j.Key]; ok {
		return ErrDuplicate
	}
	select {
	case m.rememberQ <- j:
		m.pending[j.Key] = struct{}{}
		return nil
	default:
		return ErrQueueFull
	}
}

// Flush is the barrier of the remember queue (see Queue). It waits behind the jobs of the queue only, never behind the queries.
func (m *Manager) Flush(ctx context.Context) error {
	b := make(chan struct{})
	select {
	case m.rememberQ <- Job{barrier: b}:
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ctx.Done():
		return ErrStopped
	}
	select {
	case <-b:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ctx.Done():
		return ErrStopped
	}
}

//---------------------------------------------------------------------------------------------

// loop is the goroutine of the manager: the query queue first, the remember queue only when no query is waiting.
func (m *Manager) loop() {
	defer close(m.done)
	for m.ctx.Err() == nil {
		select {
		case req := <-m.queryQ:
			m.serveQuery(req)
			continue
		default:
		}
		select {
		case <-m.ctx.Done():
			return
		case req := <-m.queryQ:
			m.serveQuery(req)
		case j := <-m.rememberQ:
			m.serveJob(j)
		}
	}
}

func (m *Manager) serveQuery(req queryRequest) {
	if req.ctx.Err() != nil { // the caller gave up while the request waited
		return
	}
	p, err := Call(req.ctx,
		m.resolver,
		req.q,
		m.timeout)
	req.reply <- queryReply{
		place: p,
		err:   err,
	}
}

func (m *Manager) serveJob(j Job) {
	if j.barrier != nil {
		close(j.barrier)
		return
	}
	defer func() {
		m.mu.Lock()
		delete(m.pending, j.Key)
		m.mu.Unlock()
	}()
	started := time.Now()
	p, err := Call(m.ctx,
		m.resolver,
		j.Query,
		m.timeout)
	if m.ctx.Err() != nil { // stopped while resolving: the answer is not the resolver's
		return
	}
	defer func() { // a panic in Done (the code of the embedding program) must not kill the manager
		if rec := recover(); rec != nil {
			log.WithField("resolver", m.resolver.Name()).Errorf("geo: a Done function panicked: %v", rec)
		}
	}()
	j.Done(m.ctx, Outcome{
		Place:   p,
		Err:     err,
		Started: started,
		Latency: time.Since(started),
	})
}

// Call asks r for the place with a timeout. A panic in r (the code of the embedding program) is an error, not a crash, and a place
// that is not valid is an error too.
func Call(ctx context.Context,
	r Resolver,
	q Query,
	timeout time.Duration) (p *Place, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if rec := recover(); rec != nil {
			p, err = nil, fmt.Errorf("geo resolver %q panicked: %v",
				r.Name(),
				rec)
		}
	}()
	p, err = r.Resolve(ctx, q)
	if err == nil && p == nil {
		return nil, fmt.Errorf("geo resolver %q returned no place and no error", r.Name())
	}
	if err == nil {
		err = p.Validate()
	}
	return p, err
}
