package training

// The training monitor: for each training job started through the training API (and each unfinished one found at startup) it
// follows the sidecar's SSE stream of the job's progress, stores every event (gb_training_job_events), keeps the job's
// row (gb_training_job_runs) up to date, and forwards the events to the SSE streams (api/training EventsOf).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

// Sidecar is what the training API needs from the sidecar; *sidecar.Client is one.
type Sidecar interface {
	JobStarter
	JobCanceller
	Inferer
	StreamTrainingJobEvents(ctx context.Context, jobID string, from int, fn func(sidecar.TrainingEvent) error) error
}

// FeedEvent is an event as the admin UI receives it: the sidecar's event and which job it is of.
type FeedEvent struct {
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
	db       *sql.DB
	sc       Sidecar // nil: jobs are not followed
	maxWatch time.Duration
	retry    time.Duration

	mu       sync.Mutex
	subs     map[*Subscription]struct{}
	watching map[int64]struct{} // by run ID
}

// Subscription is a subscriber to the monitor's events: they arrive on C.
type Subscription struct {
	runID int64 // 0: every run
	C     chan FeedEvent
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

// NewMonitor returns a monitor of the training jobs in db, which runs until ctx is done. It resumes following
// the jobs it holds as unfinished. sc may be nil: the monitor then only serves the stored events.
func NewMonitor(ctx context.Context, db *sql.DB, sc Sidecar, opts ...MonitorOption) *Monitor {
	m := &Monitor{
		ctx: ctx, db: db, sc: sc, maxWatch: DefaultWatchTimeout, retry: DefaultRetryDelay,
		subs: map[*Subscription]struct{}{}, watching: map[int64]struct{}{},
	}
	for _, o := range opts {
		o(m)
	}
	if sc != nil {
		if runs, err := trainingdb.ListTrainingJobRuns(ctx, db, 0, true); err != nil {
			log.WithError(err).Error("training: list unfinished training jobs failed")
		} else {
			for _, r := range runs {
				m.Watch(r)
			}
		}
	}
	return m
}

// Watch starts following the training job run (a no-op if it is already followed, or the monitor has no sidecar).
func (m *Monitor) Watch(run trainingdb.TrainingJobRun) {
	if m == nil || m.sc == nil {
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

// follow streams the events of the job from the sidecar until it ends, the deadline passes or the monitor's context is done.
func (m *Monitor) follow(run trainingdb.TrainingJobRun) {
	l := log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID})
	deadline := run.StartedAt.Add(m.maxWatch)
	from, err := trainingdb.NextTrainingJobEventSeq(m.ctx, m.db, run.ID)
	if err != nil {
		l.WithError(err).Error("training: training job: reading the stored events failed")
		return
	}
	delay := m.retry
	for m.ctx.Err() == nil {
		progressed, finished := false, false
		err := m.sc.StreamTrainingJobEvents(m.ctx, run.JobID, from, func(ev sidecar.TrainingEvent) error {
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
			l.WithError(err).Warn("training: training job: sidecar event stream ended with an error")
		}
		if time.Now().After(deadline) {
			l.Warn("training: training job: no end reported in time, marking it failed")
			ev := sidecar.TrainingEvent{Seq: from, Type: sidecar.EventStatus, Status: trainingdb.TrainingJobFailed,
				Error: fmt.Sprintf("the sidecar did not report the job's end within %s", m.maxWatch), Timestamp: float64(time.Now().Unix())}
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
func (m *Monitor) record(run trainingdb.TrainingJobRun, ev sidecar.TrainingEvent) error {
	ctx := context.WithoutCancel(m.ctx) // an event that arrived is stored even while the server shuts down
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if ev.Type == sidecar.EventStatus && ev.Status == trainingdb.TrainingJobCompleted {
		// Before the event is stored: a failure here makes the stream retry the event, and the model is recorded once.
		if err := m.recordModel(ctx, run, ev); err != nil {
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
		if err := trainingdb.UpdateTrainingJobRun(ctx, m.db, run.ID, *u); err != nil {
			return err
		}
	}
	isNew, err := trainingdb.RecordTrainingJobEvent(ctx, m.db, run.ID, ev.Seq, ev.Type, string(data))
	if err != nil {
		return err
	}
	if !isNew {
		return nil
	}
	if ev.Type == sidecar.EventLog {
		log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID}).Infof("training: %s", ev.Message)
	}
	if ev.Type == sidecar.EventProgress && ev.Step != nil {
		log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID, "step": *ev.Step, "loss": ev.Loss}).Debug("training: progress")
	}
	if ev.Type == sidecar.EventStatus && ev.Status != trainingdb.TrainingJobFailed {
		log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID}).Infof("training: status %s", ev.Status)
	}
	if ev.Type == sidecar.EventStatus && ev.Status == trainingdb.TrainingJobFailed {
		log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID, "error": ev.Error}).
			Errorf("training: training job failed\n%s", ev.Traceback)
	}
	m.publish(FeedEvent{RunID: run.ID, JobID: run.JobID, TrainingEvent: ev})
	return nil
}

// recordModel stores the weights a completed job reports (the "model" of its completed event) as a trained model. A
// completed event without them (from a sidecar that does not report them yet) records nothing.
func (m *Monitor) recordModel(ctx context.Context, run trainingdb.TrainingJobRun, ev sidecar.TrainingEvent) error {
	l := log.WithFields(log.Fields{"run_id": run.ID, "job_id": run.JobID})
	info := ev.Model
	if info == nil || info.Path == "" {
		l.Warn("training: the job completed without reporting its saved model: no trained model recorded (redeploy the Modal app)")
		return nil
	}
	tm := trainingdb.TrainedModel{
		RunID: &run.ID, JobID: run.JobID, BaseModel: info.BaseModel, ModelSize: info.ModelSize, Dataset: run.Dataset,
		DatasetSHA256: info.DatasetSHA256, NumExamples: info.NumExamples, NumTrainSteps: run.NumTrainSteps, FinalLoss: ev.FinalLoss,
		Params: info.Params, Path: info.Path, SizeBytes: info.SizeBytes,
	}
	if tm.ModelSize == "" {
		tm.ModelSize = run.ModelSize
	}
	for _, f := range info.Files {
		tm.Files = append(tm.Files, trainingdb.TrainedModelFile{Path: f.Path, SizeBytes: f.SizeBytes, SHA256: f.SHA256})
	}
	id, isNew, err := trainingdb.RecordTrainedModel(ctx, m.db, tm)
	if err != nil {
		return fmt.Errorf("record trained model: %w", err)
	}
	if isNew {
		l.WithFields(log.Fields{"model_id": id, "path": tm.Path, "files": len(tm.Files), "size_bytes": tm.SizeBytes}).Info("training: trained model recorded")
	}
	return nil
}

// trainingRunUpdate is the change an event makes to its job's row (nil: none).
func trainingRunUpdate(ev sidecar.TrainingEvent) *trainingdb.TrainingJobRunUpdate {
	switch ev.Type {
	case sidecar.EventProgress:
		if ev.Step == nil {
			return nil
		}
		return &trainingdb.TrainingJobRunUpdate{Step: ev.Step, Loss: ev.Loss}
	case sidecar.EventStatus:
		u := &trainingdb.TrainingJobRunUpdate{Status: ev.Status}
		if ev.Finished() {
			now := time.Now().UTC()
			u.FinishedAt, u.FinalLoss, u.Error = &now, ev.FinalLoss, ev.Error
		}
		return u
	}
	return nil
}

// Subscribe returns a subscription to the events of one run (runID 0: of every run). cancel ends it. The
// channel is closed, without cancel, when the subscriber falls too far behind.
func (m *Monitor) Subscribe(runID int64) (*Subscription, func()) {
	s := &Subscription{runID: runID, C: make(chan FeedEvent, subBuffer)}
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

func (m *Monitor) publish(ev FeedEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for s := range m.subs {
		if s.runID != 0 && s.runID != ev.RunID {
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
