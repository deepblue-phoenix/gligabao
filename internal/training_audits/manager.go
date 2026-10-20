package training_audits

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	"github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db"
	log "github.com/sirupsen/logrus"
)

// Inferer runs a prompt through a trained model on the sidecar; *sidecar.Client is one.
type Inferer interface {
	Infer(ctx context.Context, req sidecar.InferRequest) (sidecar.InferResponse, error)
}

// Manager starts the audits of trained models and runs them in the background, one at a time per trained model. It also lists,
// reads, cancels and watches them. Audits that were still running when the server last stopped are marked errored when the
// manager is made.
type Manager struct {
	// DB holds the trained models and the audits. Sc answers the questions and LLM judges (nil: starting an audit is not possible).
	DB  *sql.DB
	Sc  Inferer
	LLM LLM
	// Ops is how the audits are stored and read.
	Ops Ops

	dir string // the training datasets directory
	ctx context.Context

	mu      sync.Mutex
	running map[int64]*runningAudit   // by trained model ID
	feeds   map[core.AuditRunID]*Feed // the live feeds of the audits that run
	wg      sync.WaitGroup
}

type runningAudit struct {
	runID  core.AuditRunID // 0 until the run is recorded
	cancel context.CancelFunc
}

// NewManager makes the manager of the audits of the trained models in d, whose datasets are in the training datasets directory
// datasetDir. sc answers the questions and llm judges (the callers check for nil). The audits stop when ctx is done. Audits that
// a restart interrupted (still pending or running in the database) are marked errored.
func NewManager(ctx context.Context, d *sql.DB, ops Ops, datasetDir string, sc Inferer, llm LLM) *Manager {
	m := &Manager{DB: d, Sc: sc, LLM: llm, Ops: ops, dir: datasetDir, ctx: ctx,
		running: map[int64]*runningAudit{}, feeds: map[core.AuditRunID]*Feed{}}
	if k, err := ops.Store.FailRunningAuditRuns(ctx, d, "interrupted: the server restarted while the audit ran"); err != nil {
		log.WithError(err).Warn("training audits: could not mark interrupted audits as errored")
	} else if k > 0 {
		log.Infof("training audits: %d audit(s) were interrupted by a restart and marked errored", k)
	}
	return m
}

// Wait blocks until the running audits have ended (after the manager's context is done, or for tests).
func (m *Manager) Wait() { m.wg.Wait() }

// Done is closed when the manager's context is done.
func (m *Manager) Done() <-chan struct{} { return m.ctx.Done() }

// Start records an audit of trained model modelID and starts it in the background, returning the recorded run (status running).
// opts.Limit keeps only the first examples of the model's dataset. Errors are *Error: KindInvalid for bad options or when the
// model's dataset cannot be read (none recorded, gone from the training datasets directory, invalid), KindNotFound for an
// unknown model, KindConflict if the model has an audit running already, KindUnavailable without a sidecar or an LLM,
// KindInternal.
func (m *Manager) Start(ctx context.Context, modelID int64, opts Options) (*core.AuditRun, error) {
	if m.Sc == nil {
		return nil, unavailableErr("sidecar is not active")
	}
	if m.LLM == nil {
		return nil, unavailableErr("no LLM is available to judge the answers (set ANTHROPIC_API_KEY)")
	}
	cfg, params, err := opts.Resolve()
	if err != nil {
		return nil, invalidErr(err.Error())
	}
	tm, err := m.getModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	ds, err := LoadDataset(m.dir, tm.Dataset)
	if err != nil {
		return nil, invalidErr(err.Error())
	}
	examples := ds.Examples
	if params.Limit > 0 && params.Limit < len(examples) {
		examples = examples[:params.Limit]
	}

	// One audit at a time per model: it would otherwise compete with itself for the model's GPU container.
	runCtx, cancel := context.WithCancel(m.ctx)
	cur := &runningAudit{cancel: cancel}
	m.mu.Lock()
	if _, busy := m.running[tm.ID]; busy {
		m.mu.Unlock()
		cancel()
		return nil, conflictErr("an audit of this model is running already")
	}
	m.running[tm.ID] = cur
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		delete(m.running, tm.ID)
		m.mu.Unlock()
		cancel()
	}

	dbCtx := context.WithoutCancel(ctx)
	started := time.Now().UTC().Truncate(time.Second)
	run := &core.AuditRun{TrainedModelID: tm.ID, Dataset: ds.Name, DatasetSHA256: ds.SHA256, Status: core.StatusRunning,
		Params: params.Params(), Counts: core.Counts{Total: len(examples), Pending: len(examples)},
		Timing: core.Timing{StartedAt: &started}}
	if err := m.Ops.Store.CreateAuditRun(dbCtx, m.DB, run); err != nil {
		release()
		log.WithError(err).WithField("model_id", tm.ID).Error("training audits: recording the audit failed")
		return nil, internalErr("failed to record the audit", err)
	}
	feed := NewFeed()
	m.mu.Lock()
	cur.runID = run.ID
	m.feeds[run.ID] = feed
	m.mu.Unlock()
	rec := *run // what the caller gets, before the goroutine works on run

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer release()
		defer m.dropFeed(rec.ID, feed)
		m.run(runCtx, tm, cfg, params, examples, run, feed)
	}()
	return &rec, nil
}

// getModel reads trained model id, as an *Error.
func (m *Manager) getModel(ctx context.Context, id int64) (*trainingdb.TrainedModel, error) {
	tm, err := training.GetModel(ctx, m.DB, id)
	var te *training.Error
	switch {
	case err == nil:
		return tm, nil
	case errors.As(err, &te) && te.Kind == training.KindNotFound:
		return nil, notFoundErr(te.Msg)
	case errors.As(err, &te):
		return nil, internalErr(te.Msg, te.Err)
	}
	return nil, internalErr("failed to read the trained model", err)
}

// Generator makes the engine's GenerateFunc of trained model tm: one sidecar inference per question, with the settings of params.
func Generator(sc Inferer, tm *trainingdb.TrainedModel, params RunParams) GenerateFunc {
	return func(ctx context.Context, prompt string) (Generation, error) {
		ctx, cancel := context.WithTimeout(ctx, training.InferTimeout)
		defer cancel()
		out, err := sc.Infer(ctx, sidecar.InferRequest{Prompt: prompt, ModelSize: tm.ModelSize, ModelPath: tm.Path,
			MaxTokens: params.MaxTokens, Temperature: &params.Temperature})
		if err != nil {
			var ie *sidecar.InferError
			if errors.As(err, &ie) {
				return Generation{}, errors.New(ie.Message)
			}
			return Generation{}, err
		}
		return Generation{Response: out.Response, InputTokens: out.InputTokens, OutputTokens: out.OutputTokens}, nil
	}
}

// run is the audit's goroutine: it runs Run, storing every finished batch, and ends the run's record.
func (m *Manager) run(ctx context.Context, tm *trainingdb.TrainedModel, cfg Config, params RunParams, examples []Example, run *core.AuditRun, feed *Feed) {
	dbCtx := context.WithoutCancel(ctx) // the writes must outlive a cancelled audit
	logf := log.WithFields(log.Fields{"model_id": tm.ID, "run_id": run.ID})
	logf.WithField("examples", len(examples)).Info("training audits: audit started")
	finish := func(status core.Status, errText string) {
		if err := m.Ops.Store.FinishAuditRun(dbCtx, m.DB, run.ID, status, errText); err != nil {
			logf.WithError(err).Error("training audits: ending the audit failed")
		}
		logf.WithField("status", status).Info("training audits: audit ended")
		m.publishEnd(dbCtx, feed, run.ID)
	}
	defer func() {
		if r := recover(); r != nil {
			logf.Errorf("training audits: audit panicked: %v", r)
			finish(core.StatusErrored, fmt.Sprintf("internal error: %v", r))
		}
	}()

	gen := Generator(m.Sc, tm, params)
	judge := memory_core.WithRecording(m.LLM, m.DB, m.Ops.CallType)
	sink := func(_ context.Context, cases []core.AuditCaseRun, p Progress) error {
		if err := m.saveBatch(dbCtx, run, cases, p); err != nil {
			return err
		}
		rc := *run
		feed.Batch(rc, cases)
		return nil
	}
	cfg.OnAnswer = feed.Answer
	_, err := Run(memory_core.WithStage(ctx, StageJudge), cfg, examples, gen, judge, sink)
	switch {
	case err == nil:
		finish(core.StatusCompleted, "")
	case errors.Is(err, context.Canceled) && m.ctx.Err() != nil:
		finish(core.StatusErrored, "interrupted: the server shut down while the audit ran")
	case errors.Is(err, context.Canceled):
		finish(core.StatusCancelled, "")
	default:
		logf.WithError(err).Warn("training audits: audit failed")
		finish(core.StatusErrored, err.Error())
	}
}

// saveBatch writes the case runs of a batch and the run's progress after it, in one transaction, and sets the progress on run.
func (m *Manager) saveBatch(ctx context.Context, run *core.AuditRun, cases []core.AuditCaseRun, p Progress) error {
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save batch: %w", err)
	}
	defer tx.Rollback()
	for i := range cases {
		cases[i].RunID = run.ID
		if err := m.Ops.Store.CreateAuditCaseRun(ctx, tx, &cases[i]); err != nil {
			return err
		}
	}
	next := *run
	p.Apply(&next)
	if err := m.Ops.Store.UpdateAuditRun(ctx, tx, &next); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save batch: %w", err)
	}
	*run = next
	return nil
}

// dropFeed forgets the live feed of an audit that is over.
func (m *Manager) dropFeed(id core.AuditRunID, f *Feed) {
	m.mu.Lock()
	if m.feeds[id] == f {
		delete(m.feeds, id)
	}
	m.mu.Unlock()
}

// publishEnd announces the end of audit id on its feed, with the run as stored.
func (m *Manager) publishEnd(ctx context.Context, f *Feed, id core.AuditRunID) {
	if run, err := m.Ops.Store.GetAuditRun(ctx, m.DB, id); err == nil {
		f.End(*run)
	} else {
		log.WithError(err).WithField("run_id", id).Warn("training audits: read the ended audit for its feed failed")
		f.End(core.AuditRun{ID: id})
	}
}

// ListRuns returns the audits of trained model modelID (without their case runs), newest first, at most limit. Errors are *Error:
// KindNotFound for an unknown model, KindInternal.
func (m *Manager) ListRuns(ctx context.Context, modelID int64, limit int) ([]core.AuditRun, error) {
	if _, err := m.getModel(ctx, modelID); err != nil {
		return nil, err
	}
	list, err := m.Ops.Store.ListAuditRuns(ctx, m.DB, modelID, limit)
	if err != nil {
		log.WithError(err).Error("training audits: list audits failed")
		return nil, internalErr("failed to list the audits", err)
	}
	return list, nil
}

// GetRun reads audit id, without its case runs. Errors are *Error: KindNotFound, KindInternal.
func (m *Manager) GetRun(ctx context.Context, id core.AuditRunID) (*core.AuditRun, error) {
	run, err := m.Ops.Store.GetAuditRun(ctx, m.DB, id)
	if errors.Is(err, db.ErrNotFound) {
		return nil, notFoundErr("unknown audit")
	} else if err != nil {
		log.WithError(err).Error("training audits: read audit failed")
		return nil, internalErr("failed to read the audit", err)
	}
	return run, nil
}

// GetRunWithCaseRuns reads audit id with its CaseRuns: those stored so far, in dataset order. Errors are those of GetRun.
func (m *Manager) GetRunWithCaseRuns(ctx context.Context, id core.AuditRunID) (*core.AuditRun, error) {
	run, err := m.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if run.CaseRuns, err = m.caseRuns(ctx, id); err != nil {
		return nil, err
	}
	return run, nil
}

func (m *Manager) caseRuns(ctx context.Context, id core.AuditRunID) ([]core.AuditCaseRun, error) {
	cases, err := m.Ops.Store.ListAuditCaseRuns(ctx, m.DB, id)
	if err != nil {
		log.WithError(err).Error("training audits: list audit case runs failed")
		return nil, internalErr("failed to read the audit's case runs", err)
	}
	return cases, nil
}

// CancelRun stops a running audit (it ends as cancelled, keeping the batches done so far) and returns it as it was. Errors are
// *Error: KindNotFound for an unknown audit, KindConflict when it is not running.
func (m *Manager) CancelRun(ctx context.Context, id core.AuditRunID) (*core.AuditRun, error) {
	run, err := m.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if run.Status != core.StatusRunning && run.Status != core.StatusPending {
		return nil, conflictErr("the audit is not running")
	}
	m.mu.Lock()
	cur := m.running[run.TrainedModelID]
	m.mu.Unlock()
	if cur == nil || cur.runID != id {
		// Marked running, but nothing runs it (it cannot happen unless the bookkeeping failed): end it.
		if err := m.Ops.Store.FinishAuditRun(ctx, m.DB, id, core.StatusCancelled, ""); err != nil {
			log.WithError(err).Error("training audits: ending the audit failed")
		}
	} else {
		cur.cancel()
	}
	return run, nil
}

// Watch is an audit as a live feed starts: the run and its case runs so far, then Events (nil if nothing is running the audit
// any more: it is as stored).
type Watch struct {
	Run      *core.AuditRun // without CaseRuns
	CaseRuns []core.AuditCaseRun
	// Events are the events after the snapshot. The channel is closed when the feed ends or the watcher fell too far behind.
	Events chan Event
	feed   *Feed
}

// Close ends the watch.
func (w *Watch) Close() {
	if w.Events != nil {
		w.feed.Unsubscribe(w.Events)
	}
}

// Watch starts watching audit id: the snapshot (the run and all of its case runs so far: the ones stored, and the ones the model
// answered that the judge has not scored yet, as StatusRunning), and the events that follow while the audit goes on. The caller
// must Close it. Errors are *Error: KindNotFound for an unknown audit, KindInternal.
func (m *Manager) Watch(ctx context.Context, id core.AuditRunID) (*Watch, error) {
	// Subscribe before reading the database, so no event falls between the two; the overlap is harmless: the case runs are
	// applied by idx, and the stored version wins.
	m.mu.Lock()
	feed := m.feeds[id]
	m.mu.Unlock()
	w := &Watch{feed: feed}
	var pending []core.AuditCaseRun
	if feed != nil {
		pending, w.Events = feed.Subscribe()
	}
	fail := func(err error) (*Watch, error) { w.Close(); return nil, err }
	run, err := m.GetRun(ctx, id)
	if err != nil {
		return fail(err)
	}
	cases, err := m.caseRuns(ctx, id)
	if err != nil {
		return fail(err)
	}
	stored := make(map[int]bool, len(cases))
	for _, c := range cases {
		stored[c.Idx] = true
	}
	for _, c := range pending {
		if !stored[c.Idx] {
			cases = append(cases, c)
		}
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Idx < cases[j].Idx })
	w.Run, w.CaseRuns = run, cases
	return w, nil
}
