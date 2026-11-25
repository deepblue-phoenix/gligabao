package training

// The training monitor: for each training job started through the training API (and each unfinished one found at startup) it
// follows the events of the job that its backend streams (the ML sidecar's SSE stream, sargo's status, see backend.go), stores every event (gb_training_job_events), keeps the job's
// row (gb_training_job_runs) up to date, and forwards the events to the SSE streams (api/training EventsOf).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/ml_sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/monitoring"
	log "github.com/sirupsen/logrus"
)

// MLSidecar is what the training API needs from the ML sidecar; *ml_sidecar.Client is one.
type MLSidecar interface {
	MLSidecarJobs
	Inferer
}

const (
	// DefaultWatchTimeout is how long after a job started the monitor waits for it to report that it ended
	// (the ML sidecar's train timeout is 2 hours, ML_SIDECAR_TRAIN_TIMEOUT_S); then the job is marked failed.
	DefaultWatchTimeout = 3 * time.Hour
	// DefaultRetryDelay is how long the monitor waits before it reconnects to an ML sidecar stream that ended or failed
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
	db       *sql.DB
	backends Backends // by model type; a run of a type that has none is not followed
	maxWatch time.Duration
	retry    time.Duration

	mu       sync.Mutex
	subs     map[*Subscription]struct{}
	watching map[int64]struct{} // by run ID
}

// Subscription is a subscriber to the monitor's events: they arrive on C.
type Subscription struct {
	runID  int64  // 0: every run (of the user)
	userID string // only the events of the jobs of this user; "" (in-process users of the monitor only, never a request): every user
	C      chan monitoring.TrainingEvent
}

// MonitorOption configures a Monitor.
type MonitorOption func(*Monitor)

// WithWatchTimeout sets how long a job is followed without it reporting that it ended (DefaultWatchTimeout).
func WithWatchTimeout(d time.Duration) MonitorOption {
	return func(m *Monitor) { m.maxWatch = d }
}

// WithRetryDelay sets the base delay before reconnecting to the ML sidecar (DefaultRetryDelay).
func WithRetryDelay(d time.Duration) MonitorOption {
	return func(m *Monitor) { m.retry = d }
}

// NewMonitor returns a monitor of the training jobs in db, which runs until ctx is done. It resumes following
// the jobs it holds as unfinished that its backends run. With no backends the monitor only serves the stored events.
func NewMonitor(ctx context.Context,
	db *sql.DB,
	backends Backends,
	opts ...MonitorOption) *Monitor {
	m := &Monitor{
		ctx:      ctx,
		db:       db,
		backends: backends,
		maxWatch: DefaultWatchTimeout,
		retry:    DefaultRetryDelay,
		subs:     map[*Subscription]struct{}{},
		watching: map[int64]struct{}{},
	}
	for _, o := range opts {
		o(m)
	}
	if len(backends) != 0 {
		if runs, err := trainingdb.ListTrainingJobRuns(ctx,
			db,
			0,
			true); err != nil {
			log.WithError(err).Error("training: list unfinished training jobs failed")
		} else {
			reaped := map[string]bool{}
			for _, r := range runs {
				if b, ok := backends[r.Type()]; ok && !reaped[r.Type()+"/"+r.UserID] {
					reaped[r.Type()+"/"+r.UserID] = true
					if err := b.Reap(ctx, r.UserID); err != nil {
						log.WithError(err).WithField("user_id", r.UserID).Warn("training: reaping the runs of a stopped instance failed")
					}
				}
			}
			if runs, err = trainingdb.ListTrainingJobRuns(ctx,
				db,
				0,
				true); err != nil {
				log.WithError(err).Error("training: list unfinished training jobs failed")
			}
			for _, r := range runs {
				m.Watch(r)
			}
		}
	}
	return m
}

// Watch starts following the training job run (a no-op if it is already followed, the monitor has no backend for its model type, or
// another instance follows it).
func (m *Monitor) Watch(run trainingdb.TrainingJobRun) {
	if m == nil {
		return
	}
	if b, ok := m.backends[run.Type()]; !ok || !b.Owns(run) {
		return
	}
	key := run.ID
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
		m.follow(run)
	}()
}

// follow streams the events of the job from its backend until it ends, the deadline passes or the monitor's context is done.
func (m *Monitor) follow(run trainingdb.TrainingJobRun) {
	b := m.backends[run.Type()]
	l := log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID})
	deadline := run.StartedAt.Add(m.maxWatch)
	from, err := trainingdb.NextTrainingJobEventSeq(m.ctx,
		m.db,
		run.ID)
	if err != nil {
		l.WithError(err).Error("training: training job: reading the stored events failed")
		return
	}
	delay := m.retry
	for m.ctx.Err() == nil {
		progressed, finished := false, false
		err := b.Stream(m.ctx,
			run,
			from,
			func(ev monitoring.TrainingEvent) error {
				progressed = true
				if err := m.record(run, ev); err != nil {
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
			l.WithError(err).Warn("training: training job: the event stream of the backend ended with an error")
			streamErr := jobEvent(run, monitoring.EventStreamError)
			streamErr.Error = err.Error()
			m.Emit(streamErr)
		}
		if time.Now().After(deadline) {
			l.Warn("training: training job: no end reported in time, marking it failed")
			ev := ml_sidecar.TrainingEvent{
				Seq:       from,
				Type:      ml_sidecar.EventStatus,
				Status:    trainingdb.TrainingJobFailed,
				Error:     fmt.Sprintf("the job did not report its end within %s", m.maxWatch),
				Timestamp: float64(time.Now().Unix()),
			}
			if err := m.record(run, ev); err != nil {
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

// record updates the job's row from the event, stores the event and forwards it to the subscribers.
func (m *Monitor) record(run trainingdb.TrainingJobRun,
	ev ml_sidecar.TrainingEvent) error {
	ctx := context.WithoutCancel(m.ctx) // an event that arrived is stored even while the server shuts down
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	var modelID int64
	if ev.Type == ml_sidecar.EventStatus && ev.Status == trainingdb.TrainingJobCompleted {
		// Before the event is stored: a failure here makes the stream retry the event, and the model is recorded once.
		if modelID, err = m.recordModel(ctx,
			run,
			ev); err != nil {
			return err
		}
	}
	// The job's row is updated BEFORE the event is stored. The stored event is what marks it as seen: a retry of an event
	// that is stored already (isNew is false below) does nothing. With the two the other way round, a failure of the
	// update (or a crash) between them left the event stored and the row unchanged, and nothing ever applied it again: for
	// a "completed" event, a job that stays "training" in the database for good although the monitor has stopped following
	// it. This way a failure of the insert only makes the stream resend the event, and the update is safe to repeat: it
	// sets the row's step, loss, status and end to the values of this event (the retry is of the last event only: the
	// stream resumes after the last stored seq).
	if u := trainingRunUpdate(ev); u != nil {
		if err := trainingdb.UpdateTrainingJobRun(ctx,
			m.db,
			run.ID,
			*u); err != nil {
			return err
		}
	}
	isNew, err := trainingdb.RecordTrainingJobEvent(ctx,
		m.db,
		run.ID,
		ev.Seq,
		ev.Type,
		string(data))
	if err != nil {
		return err
	}
	if !isNew {
		return nil
	}
	if ev.Finished() {
		m.backends[run.Type()].Ended(ctx,
			run,
			ev,
			modelID)
	}
	if ev.Type == ml_sidecar.EventLog {
		log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID}).Infof("training: %s", ev.Message)
	}
	if ev.Type == ml_sidecar.EventProgress && ev.Step != nil {
		log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID, "step": *ev.Step, "loss": ev.Loss}).Debug("training: progress")
	}
	if ev.Type == ml_sidecar.EventStatus && ev.Status != trainingdb.TrainingJobFailed {
		log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID}).Infof("training: status %s", ev.Status)
	}
	if ev.Type == ml_sidecar.EventStatus && ev.Status == trainingdb.TrainingJobFailed {
		log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID, "error": ev.Error}).
			Errorf("training: training job failed\n%s", ev.Traceback)
	}
	m.Emit(ofRun(run, ev))
	return nil
}

// recordModel stores the model a completed job made (the backend says what it is) as a trained model; it returns its ID (0: the job made
// none). The model of a job that was recorded already is not recorded twice.
func (m *Monitor) recordModel(ctx context.Context,
	run trainingdb.TrainingJobRun,
	ev ml_sidecar.TrainingEvent) (int64, error) {
	l := log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID})
	b := m.backends[run.Type()]
	rec, err := b.Model(ctx,
		run,
		ev)
	if err != nil {
		return 0, fmt.Errorf("model of the completed job: %w", err)
	}
	if rec == nil {
		return 0, nil
	}
	tm := rec.Model
	tm.ModelType = run.Type()
	id, isNew, err := trainingdb.RecordTrainedModel(ctx,
		m.db,
		tm)
	if err != nil {
		return 0, fmt.Errorf("record trained model: %w", err)
	}
	if rec.Done != nil {
		if err := rec.Done(ctx, id); err != nil {
			return 0, fmt.Errorf("finish the trained model: %w", err)
		}
	}
	if isNew {
		l.WithFields(log.Fields{"model_id": id, "path": tm.Path, "files": len(tm.Files), "size_bytes": tm.SizeBytes}).Info("training: trained model recorded")
		recorded := jobEvent(run, monitoring.EventModelRecorded)
		recorded.ModelID = id
		recorded.Model = rec.Info
		m.Emit(recorded)
	}
	return id, nil
}

// trainingRunUpdate is the change an event makes to its job's row (nil: none).
func trainingRunUpdate(ev ml_sidecar.TrainingEvent) *trainingdb.TrainingJobRunUpdate {
	switch ev.Type {
	case ml_sidecar.EventProgress:
		if ev.Step == nil {
			return nil
		}
		return &trainingdb.TrainingJobRunUpdate{
			Step: ev.Step,
			Loss: ev.Loss,
		}
	case ml_sidecar.EventStatus:
		u := &trainingdb.TrainingJobRunUpdate{
			Status: ev.Status,
		}
		if ev.Finished() {
			now := time.Now().UTC()
			u.FinishedAt, u.FinalLoss, u.Error = &now, ev.FinalLoss, ev.Error
		}
		return u
	}
	return nil
}

// Emit sends the event to the hooks (monitoring.EmitTrainingEvent) and to the subscribers of the feed: the one way out of every training event.
// It is safe on a nil monitor, which has no subscribers.
func (m *Monitor) Emit(ev monitoring.TrainingEvent) {
	monitoring.EmitTrainingEvent(ev)
	if m != nil {
		m.publish(ev)
	}
}

// Subscribe returns a subscription to the events of one run (runID 0: of every run) of the jobs of userID (a request always gives its
// user; "" is for code that runs inside the server and follows every user). cancel ends it. The channel is closed, without cancel, when
// the subscriber falls too far behind.
func (m *Monitor) Subscribe(runID int64,
	userID string) (*Subscription, func()) {
	s := &Subscription{
		runID:  runID,
		userID: userID,
		C:      make(chan monitoring.TrainingEvent, subBuffer),
	}
	m.mu.Lock()
	m.subs[s] = struct{}{}
	m.mu.Unlock()
	return s, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.subs[s]; ok {
			delete(m.subs, s)
			close(s.C)
		}
	}
}

// Done is closed when the monitor's context is done.
func (m *Monitor) Done() <-chan struct{} { return m.ctx.Done() }

func (m *Monitor) publish(ev monitoring.TrainingEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for s := range m.subs {
		if s.runID != 0 && s.runID != ev.RunID {
			continue
		}
		if s.userID != "" && s.userID != ev.UserID {
			continue
		}
		select {
		case s.C <- ev:
		default: // too slow: drop it; it reconnects and is caught up from the database
			delete(m.subs, s)
			close(s.C)
		}
	}
}
