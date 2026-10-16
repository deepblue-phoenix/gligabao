package training

// The training monitor: for each training job started through the training API (and each unfinished one found at startup) it
// follows the sidecar's SSE stream of the job's progress, stores every event (gb_training_job_events), keeps the job's
// row (gb_training_job_runs) up to date, and forwards the events to the SSE streams (EventsOf).

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
	log "github.com/sirupsen/logrus"
)

// Sidecar is what the training handlers need from the sidecar; *sidecar.Client is one.
type Sidecar interface {
	JobStarter
	StreamTrainingJobEvents(ctx context.Context, jobID string, from int, fn func(sidecar.TrainingEvent) error) error
}

// FeedEvent is an event as the admin UI receives it: the sidecar's event and which job it is of.
type FeedEvent struct {
	DB    string `json:"db"`
	RunID int64  `json:"run_id"`
	JobID string `json:"job_id"`
	sidecar.TrainingEvent
}

const (
	// DefaultWatchTimeout is how long after a job started the monitor waits for it to report that it ended
	// (the sidecar's train timeout is 2 hours, SIDECAR_TRAIN_TIMEOUT_S); then the job is marked failed.
	DefaultWatchTimeout = 3 * time.Hour
	// DefaultRetryDelay is how long the monitor waits before it reconnects to a sidecar stream that ended or failed
	// (doubled while nothing arrives, up to maxRetryDelay).
	DefaultRetryDelay = time.Second
	maxRetryDelay     = 30 * time.Second
	// subBuffer is how many events a subscriber can fall behind before it is dropped (it then reconnects and is
	// caught up from the database).
	subBuffer = 256
)

// Monitor follows the training jobs; see the file comment. Its methods are safe for concurrent use.
type Monitor struct {
	ctx      context.Context
	dbs      []Database
	sc       Sidecar // nil: jobs are not followed
	maxWatch time.Duration
	retry    time.Duration

	mu       sync.Mutex
	subs     map[*sub]struct{}
	watching map[watchKey]struct{}
}

type watchKey struct {
	db    string
	runID int64
}

type sub struct {
	db    string
	runID int64 // 0: every run
	ch    chan FeedEvent
}

// MonitorOption configures a Monitor.
type MonitorOption func(*Monitor)

// WithWatchTimeout sets how long a job is followed without it reporting that it ended (DefaultWatchTimeout).
func WithWatchTimeout(d time.Duration) MonitorOption {
	return func(m *Monitor) { m.maxWatch = d }
}

// WithRetryDelay sets the base delay before reconnecting to the sidecar (DefaultRetryDelay).
func WithRetryDelay(d time.Duration) MonitorOption {
	return func(m *Monitor) { m.retry = d }
}

// NewMonitor returns a monitor of the training jobs of dbs, which runs until ctx is done. It resumes following
// the jobs the databases hold as unfinished. sc may be nil: the monitor then only serves the stored events.
func NewMonitor(ctx context.Context, dbs []Database, sc Sidecar, opts ...MonitorOption) *Monitor {
	m := &Monitor{
		ctx: ctx, dbs: dbs, sc: sc, maxWatch: DefaultWatchTimeout, retry: DefaultRetryDelay,
		subs: map[*sub]struct{}{}, watching: map[watchKey]struct{}{},
	}
	for _, o := range opts {
		o(m)
	}
	if sc != nil {
		for _, n := range dbs {
			runs, err := relational.ListTrainingJobRuns(ctx, n.DB, 0, true)
			if err != nil {
				log.WithError(err).WithField("db", n.Name).Error("training: list unfinished training jobs failed")
				continue
			}
			for _, r := range runs {
				m.Watch(n, r)
			}
		}
	}
	return m
}

// Watch starts following the training job run in n (a no-op if it is already followed, or the monitor has no sidecar).
func (m *Monitor) Watch(n Database, run relational.TrainingJobRun) {
	if m == nil || m.sc == nil {
		return
	}
	key := watchKey{n.Name, run.ID}
	m.mu.Lock()
	if _, ok := m.watching[key]; ok {
		m.mu.Unlock()
		return
	}
	m.watching[key] = struct{}{}
	m.mu.Unlock()
	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.watching, key)
			m.mu.Unlock()
		}()
		m.follow(n, run)
	}()
}

// follow streams the events of the job from the sidecar until it ends, the deadline passes or the monitor's context is done.
func (m *Monitor) follow(n Database, run relational.TrainingJobRun) {
	l := log.WithFields(log.Fields{"db": n.Name, "run_id": run.ID, "job_id": run.JobID})
	deadline := run.StartedAt.Add(m.maxWatch)
	from, err := relational.NextTrainingJobEventSeq(m.ctx, n.DB, run.ID)
	if err != nil {
		l.WithError(err).Error("training: training job: reading the stored events failed")
		return
	}
	delay := m.retry
	for m.ctx.Err() == nil {
		progressed, finished := false, false
		err := m.sc.StreamTrainingJobEvents(m.ctx, run.JobID, from, func(ev sidecar.TrainingEvent) error {
			progressed = true
			if err := m.record(n, run, ev); err != nil {
				return err
			}
			from = ev.Seq + 1
			finished = ev.Finished()
			return nil
		})
		if finished || m.ctx.Err() != nil {
			return
		}
		if err != nil {
			l.WithError(err).Warn("training: training job: sidecar event stream ended with an error")
		}
		if time.Now().After(deadline) {
			l.Warn("training: training job: no end reported in time, marking it failed")
			ev := sidecar.TrainingEvent{Seq: from, Type: sidecar.EventStatus, Status: relational.TrainingJobFailed,
				Error: fmt.Sprintf("the sidecar did not report the job's end within %s", m.maxWatch), Timestamp: float64(time.Now().Unix())}
			if err := m.record(n, run, ev); err != nil {
				l.WithError(err).Error("training: training job: recording the timeout failed")
			}
			return
		}
		if progressed {
			delay = m.retry
		} else {
			delay = min(delay*2, maxRetryDelay)
		}
		select {
		case <-time.After(delay):
		case <-m.ctx.Done():
			return
		}
	}
}

// record stores the event, updates the job's row from it and forwards it to the subscribers.
func (m *Monitor) record(n Database, run relational.TrainingJobRun, ev sidecar.TrainingEvent) error {
	ctx := context.WithoutCancel(m.ctx) // an event that arrived is stored even while the server shuts down
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	isNew, err := relational.RecordTrainingJobEvent(ctx, n.DB, run.ID, ev.Seq, ev.Type, string(data))
	if err != nil {
		return err
	}
	if !isNew {
		return nil
	}
	if ev.Type == sidecar.EventStatus && ev.Status == relational.TrainingJobFailed {
		log.WithFields(log.Fields{"db": n.Name, "run_id": run.ID, "job_id": run.JobID, "error": ev.Error}).
			Errorf("training: training job failed\n%s", ev.Traceback)
	}
	if u := trainingRunUpdate(ev); u != nil {
		if err := relational.UpdateTrainingJobRun(ctx, n.DB, run.ID, *u); err != nil {
			return err
		}
	}
	m.publish(FeedEvent{DB: n.Name, RunID: run.ID, JobID: run.JobID, TrainingEvent: ev})
	return nil
}

// trainingRunUpdate is the change an event makes to its job's row (nil: none).
func trainingRunUpdate(ev sidecar.TrainingEvent) *relational.TrainingJobRunUpdate {
	switch ev.Type {
	case sidecar.EventProgress:
		if ev.Step == nil {
			return nil
		}
		return &relational.TrainingJobRunUpdate{Step: ev.Step, Loss: ev.Loss}
	case sidecar.EventStatus:
		u := &relational.TrainingJobRunUpdate{Status: ev.Status}
		if ev.Finished() {
			now := time.Now().UTC()
			u.FinishedAt, u.FinalLoss, u.Error = &now, ev.FinalLoss, ev.Error
		}
		return u
	}
	return nil
}

// subscribe returns a subscription to the events of one run of database db (runID 0: of every run). cancel ends it. The
// channel is closed, without cancel, when the subscriber falls too far behind.
func (m *Monitor) subscribe(db string, runID int64) (*sub, func()) {
	s := &sub{db: db, runID: runID, ch: make(chan FeedEvent, subBuffer)}
	m.mu.Lock()
	m.subs[s] = struct{}{}
	m.mu.Unlock()
	return s, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.subs[s]; ok {
			delete(m.subs, s)
			close(s.ch)
		}
	}
}

func (m *Monitor) publish(ev FeedEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for s := range m.subs {
		if s.db != ev.DB || (s.runID != 0 && s.runID != ev.RunID) {
			continue
		}
		select {
		case s.ch <- ev:
		default: // too slow: drop it; it reconnects and is caught up from the database
			delete(m.subs, s)
			close(s.ch)
		}
	}
}
