package training

// Runs of trained models judged by an LLM: the evals. An eval is a TrainedModelRun that asks the model
// the question of each case and has the external LLM (the one used for knowledge ingestion) judge, a batch of question/answer
// pairs per LLM call, how close the answers are to the expected ones. This file is the manager and the background runner, and runs_stream.go the stream. An eval (the evals of internal/evals/training) asks
// the cases of an eval (bin/evals_training/*.json), which the model never saw in training: it measures how well the model generalizes.
// Its cases have names and it can be part of an experiment. (The training data audits, which ask the model the examples of its OWN
// fine-tuning dataset, are in internal/training_audits.)
//
// The logic is the subsystem internal/evals/training; this file wires it to the sidecar (the model), the LLM (the judge) and the database
// (evalsdb.TrainedModelRun, which holds the progress and the result), and runs it in the background: starting a run answers at
// once and the run is followed by reading its row (or its live feed).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	trainingeval "github.com/deepblue-phoenix/gligabao/internal/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

const (
	// CallEvalJudge is the type of the runs (gb_inferences) the judge's LLM calls of an eval are recorded as; StageJudge is their stage.
	CallEvalJudge = "training_eval_judge"
	StageJudge    = "judge"

	defaultRunMaxTokens = 1024
	// defaultRunTemperature is low: a run checks what the model learned, not how varied it can be.
	defaultRunTemperature = 0.1
)

// RunParams are the settings a run was started with (the "params" of its row).
type RunParams struct {
	BatchSize   int     `json:"batch_size"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
	// Limit is the number of questions asked: the first ones of the eval or the dataset (0 = all of them).
	Limit int `json:"limit"`
	// Cases are the names of the eval's cases that were asked, when only some were (an eval only).
	Cases []string `json:"cases,omitempty"`
}

// RunOptions are the fields of the body of the request that starts an eval.
type RunOptions struct {
	Limit       int      `json:"limit"`
	BatchSize   int      `json:"batch_size"`
	JudgeModel  string   `json:"judge_model"`
	MaxTokens   int      `json:"max_tokens"`
	Temperature *float64 `json:"temperature"`
}

// Resolve checks the options and returns the engine's config and the params to store; the error is safe to show.
func (o RunOptions) Resolve() (trainingeval.Config, RunParams, error) {
	cfg, err := trainingeval.Config{BatchSize: o.BatchSize, JudgeModel: o.JudgeModel}.Normalize()
	switch {
	case err != nil:
		return cfg, RunParams{}, err
	case o.Limit < 0:
		return cfg, RunParams{}, errors.New("limit must be 0 (all) or more")
	}
	if err := ValidateGeneration(o.MaxTokens, o.Temperature); err != nil {
		return cfg, RunParams{}, err
	}
	p := RunParams{BatchSize: cfg.BatchSize, MaxTokens: o.MaxTokens, Temperature: defaultRunTemperature, Limit: o.Limit}
	if p.MaxTokens == 0 {
		p.MaxTokens = defaultRunMaxTokens
	}
	if o.Temperature != nil {
		p.Temperature = *o.Temperature
	}
	return cfg, p, nil
}

// RunOps is what differs between the two kinds of run in the database: the functions that store and read them.
type RunOps struct {
	Kind        string // evalsdb.TrainedModelKindEval
	CallType    string // the type of the judge's calls in gb_inferences
	Record      func(ctx context.Context, d *sql.DB, r evalsdb.TrainedModelRun) (int64, error)
	Save        func(ctx context.Context, d *sql.DB, r evalsdb.TrainedModelRun, cases []evalsdb.TrainedModelCase) error
	Finish      func(ctx context.Context, d *sql.DB, id int64, status, errText string) error
	FailRunning func(ctx context.Context, d *sql.DB, reason string) (int64, error)
	Get         func(ctx context.Context, d *sql.DB, id int64) (*evalsdb.TrainedModelRun, error)
	List        func(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]evalsdb.TrainedModelRun, error)
	Cases       func(ctx context.Context, d *sql.DB, runID int64) ([]evalsdb.TrainedModelCase, error)
}

// Noun is what the kind is called in messages ("eval"); its URLs and the list in its responses are the plural.
func (o RunOps) Noun() string   { return o.Kind }
func (o RunOps) Plural() string { return o.Kind + "s" }

// EvalOps is the kind of run of the evals.
var EvalOps = RunOps{
	Kind:        evalsdb.TrainedModelKindEval,
	CallType:    CallEvalJudge,
	Record:      evalsdb.RecordTrainedModelEvalRun,
	Save:        evalsdb.SaveTrainedModelEvalRunBatch,
	Finish:      evalsdb.FinishTrainedModelEvalRun,
	FailRunning: evalsdb.FailRunningTrainedModelEvalRuns,
	Get:         evalsdb.GetTrainedModelEvalRun,
	List:        evalsdb.ListTrainedModelEvalRuns,
	Cases:       evalsdb.ListTrainedModelEvalCases,
}

// RunManager starts the runs of one kind and runs them in the background, one at a time per trained model. Runs still running when
// the server last stopped are marked error when the manager is made. The HTTP API's EvalManager is one.
type RunManager struct {
	Ops RunOps
	// DB holds the trained models; Sc answers the questions and LLM judges (nil: starting a run is not possible).
	DB  *sql.DB
	Sc  Inferer
	LLM memory_core.LLM
	ctx context.Context

	mu      sync.Mutex
	running map[int64]*runningRun
	streams map[int64]*RunStream // the live feeds (runs_stream.go) of the runs that run
	wg      sync.WaitGroup
}

type runningRun struct {
	runID  int64 // 0 until the run's row is made
	cancel context.CancelFunc
}

// NewRunManager makes the manager of the runs of the trained models in db. sc answers the questions and llm judges (the
// callers check for nil). The runs stop when ctx is done. Runs that a restart interrupted (still "running" in the
// database) are marked error.
func NewRunManager(ctx context.Context, db *sql.DB, sc Inferer, llm memory_core.LLM, ops RunOps) *RunManager {
	m := &RunManager{Ops: ops, ctx: ctx, DB: db, Sc: sc, LLM: llm, running: map[int64]*runningRun{}, streams: map[int64]*RunStream{}}
	if k, err := ops.FailRunning(ctx, m.DB, "interrupted: the server restarted while the "+ops.Noun()+" ran"); err != nil {
		log.WithError(err).Warnf("training: could not mark interrupted %ss as error", ops.Noun())
	} else if k > 0 {
		log.Infof("training: %d %s(s) were interrupted by a restart and marked error", k, ops.Noun())
	}
	return m
}

// Wait blocks until the running runs have ended (after the manager's context is done, or for tests).
func (m *RunManager) Wait() { m.wg.Wait() }

// Done is closed when the manager's context is done.
func (m *RunManager) Done() <-chan struct{} { return m.ctx.Done() }

// RunSpec is a run ready to start: what the caller works out and Start carries out.
type RunSpec struct {
	Model  *trainingdb.TrainedModel
	Config trainingeval.Config
	Params RunParams
	// Row is the run to record (Eval; the settings and NumExamples are set by Start).
	Row evalsdb.TrainedModelRun
	// Examples are the questions to ask and Names the names of the cases that go with them.
	Examples []trainingeval.Example
	Names    []string
	// AfterRecord, if set, is called with the ID of the recorded run, before it starts (an eval joins its experiment there).
	AfterRecord func(ctx context.Context, id int64) error
}

// Start records the run and starts it in the background, returning the recorded run. Errors are *Error: KindConflict if the
// model has a run of this kind already, KindInternal if it could not be recorded.
func (m *RunManager) Start(ctx context.Context, s RunSpec) (*evalsdb.TrainedModelRun, error) {
	noun, tm := m.Ops.Noun(), s.Model
	// One run at a time per model and kind: it would otherwise compete with itself for the model's GPU container.
	key := tm.ID
	runCtx, cancel := context.WithCancel(m.ctx)
	run := &runningRun{cancel: cancel}
	m.mu.Lock()
	if _, busy := m.running[key]; busy {
		m.mu.Unlock()
		cancel()
		return nil, conflictErr("an " + noun + " of this model is running already")
	}
	m.running[key] = run
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		delete(m.running, key)
		m.mu.Unlock()
		cancel()
	}

	pj, _ := json.Marshal(s.Params)
	dbCtx := context.WithoutCancel(ctx)
	row := s.Row
	row.TrainedModelID, row.Params, row.JudgeModel, row.NumExamples = tm.ID, pj, s.Config.JudgeModel, len(s.Examples)
	runID, err := m.Ops.Record(dbCtx, m.DB, row)
	if err != nil {
		release()
		log.WithError(err).WithField("model_id", tm.ID).Errorf("training: recording the %s failed", noun)
		return nil, internalErr("failed to record the "+noun, err)
	}
	fail := func(msg string, err error) error {
		release()
		_ = m.Ops.Finish(dbCtx, m.DB, runID, evalsdb.TrainedModelRunErrored, "could not start")
		log.WithError(err).WithField("run_id", runID).Errorf("training: %s", msg)
		return internalErr("failed to "+msg, err)
	}
	if s.AfterRecord != nil {
		if err := s.AfterRecord(dbCtx, runID); err != nil {
			return nil, fail("add the "+noun+" to the experiment", err)
		}
	}
	stream := NewRunStream()
	skey := runID
	m.mu.Lock()
	run.runID = runID
	m.streams[skey] = stream
	m.mu.Unlock()
	rec, err := m.Ops.Get(dbCtx, m.DB, runID)
	if err != nil {
		m.dropStream(skey, stream)
		return nil, fail("read the "+noun, err)
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer release()
		defer m.dropStream(skey, stream)
		m.run(runCtx, s, runID, stream)
	}()
	return rec, nil
}

// RunProgress is the run's row with the counters of the engine's progress: what SaveTrainedModel*RunBatch writes after a batch.
func RunProgress(runID int64, p trainingeval.Progress) evalsdb.TrainedModelRun {
	return evalsdb.TrainedModelRun{ID: runID, JudgeModel: p.JudgeModel, NumAnswered: p.Answered, NumJudged: p.Judged, NumErrored: p.Errored,
		NumCorrect: p.Correct, NumPartial: p.Partial, NumIncorrect: p.Incorrect, ScoreSum: p.ScoreSum, NumBatches: p.Batches,
		JudgeCalls: p.JudgeCalls, JudgeInputTokens: p.JudgeInputTokens, JudgeOutputTokens: p.JudgeOutputTokens, JudgeCostUSD: p.JudgeCostUSD}
}

// Generator makes the engine's GenerateFunc of trained model tm: one sidecar inference per question, with the settings of params
// (the server's runs and the command-line runner internal/evals/training/cmd ask the model the same way).
func Generator(sc Inferer, tm *trainingdb.TrainedModel, params RunParams) trainingeval.GenerateFunc {
	return func(ctx context.Context, prompt string) (trainingeval.Generation, error) {
		ctx, cancel := context.WithTimeout(ctx, InferTimeout)
		defer cancel()
		out, err := sc.Infer(ctx, sidecar.InferRequest{Prompt: prompt, ModelSize: tm.ModelSize, ModelPath: tm.Path,
			MaxTokens: params.MaxTokens, Temperature: &params.Temperature})
		if err != nil {
			var ie *sidecar.InferError
			if errors.As(err, &ie) {
				return trainingeval.Generation{}, errors.New(ie.Message)
			}
			return trainingeval.Generation{}, err
		}
		return trainingeval.Generation{Response: out.Response, InputTokens: out.InputTokens, OutputTokens: out.OutputTokens}, nil
	}
}

// CaseRow is the stored form of a case of run runID. The engine's statuses (trainingeval.CaseJudged, CaseErrored, "" for an
// answer the judge has not scored yet) are those of TrainedModelCase, so the status is copied as it is. names (an eval's) give
// the case its name.
func CaseRow(runID int64, names []string, it trainingeval.CaseResult) evalsdb.TrainedModelCase {
	c := evalsdb.TrainedModelCase{RunID: runID, Idx: it.Idx, Batch: it.Batch, Instruction: it.Instruction, Input: it.Input,
		Reference: it.Reference, Response: it.Response, Status: it.Status, Error: it.Error, Score: it.Score, Reason: it.Reason,
		InputTokens: it.InputTokens, OutputTokens: it.OutputTokens, DurationMS: it.DurationMS}
	if it.Idx >= 0 && it.Idx < len(names) {
		c.Name = names[it.Idx]
	}
	return c
}

// run is the run's goroutine: it runs trainingeval.Run, storing every finished batch, and ends the run's row.
func (m *RunManager) run(ctx context.Context, s RunSpec, runID int64, stream *RunStream) {
	noun, tm, params := m.Ops.Noun(), s.Model, s.Params
	dbCtx := context.WithoutCancel(ctx) // the writes must outlive a cancelled run
	logf := log.WithFields(log.Fields{"model_id": tm.ID, "run_id": runID, "kind": noun})
	logf.WithField("examples", len(s.Examples)).Infof("training: %s started", noun)
	finish := func(status, errText string) {
		if err := m.Ops.Finish(dbCtx, m.DB, runID, status, errText); err != nil {
			logf.WithError(err).Errorf("training: ending the %s failed", noun)
		}
		logf.WithField("status", status).Infof("training: %s ended", noun)
		m.publishEnd(dbCtx, stream, runID)
	}
	defer func() {
		if r := recover(); r != nil {
			logf.Errorf("training: %s panicked: %v", noun, r)
			finish(evalsdb.TrainedModelRunErrored, fmt.Sprintf("internal error: %v", r))
		}
	}()

	gen := Generator(m.Sc, tm, params)
	judge := memory_core.WithRecording(m.LLM, m.DB, m.Ops.CallType)
	sink := func(_ context.Context, cases []trainingeval.CaseResult, p trainingeval.Progress) error {
		rows := make([]evalsdb.TrainedModelCase, len(cases))
		for i, it := range cases {
			rows[i] = CaseRow(runID, s.Names, it)
		}
		rec := RunProgress(runID, p)
		if err := m.Ops.Save(dbCtx, m.DB, rec, rows); err != nil {
			return err
		}
		if full, err := m.Ops.Get(dbCtx, m.DB, runID); err == nil { // the whole row, for the live feed
			rec = *full
		}
		stream.Batch(rec, rows)
		return nil
	}
	cfg := s.Config
	cfg.OnAnswer = func(it trainingeval.CaseResult) { stream.Answer(CaseRow(runID, s.Names, it)) }
	_, err := trainingeval.Run(memory_core.WithStage(ctx, StageJudge), cfg, s.Examples, gen, judge, sink)
	switch {
	case err == nil:
		finish(evalsdb.TrainedModelRunCompleted, "")
	case errors.Is(err, context.Canceled) && m.ctx.Err() != nil:
		finish(evalsdb.TrainedModelRunErrored, "interrupted: the server shut down while the "+noun+" ran")
	case errors.Is(err, context.Canceled):
		finish(evalsdb.TrainedModelRunCancelled, "")
	default:
		logf.WithError(err).Warnf("training: %s failed", noun)
		finish(evalsdb.TrainedModelRunErrored, err.Error())
	}
}

// dropStream forgets the live feed of a run that is over.
func (m *RunManager) dropStream(key int64, s *RunStream) {
	m.mu.Lock()
	if m.streams[key] == s {
		delete(m.streams, key)
	}
	m.mu.Unlock()
}

// ListRuns returns the runs of trained model modelID of the database (without their cases), newest first, at most limit. Errors are *Error:
// KindNotFound for an unknown model, KindInternal.
func (m *RunManager) ListRuns(ctx context.Context, modelID int64, limit int) ([]evalsdb.TrainedModelRun, error) {
	if _, err := GetModel(ctx, m.DB, modelID); err != nil {
		return nil, err
	}
	list, err := m.Ops.List(ctx, m.DB, modelID, limit)
	if err != nil {
		log.WithError(err).Errorf("training: list %ss failed", m.Ops.Noun())
		return nil, internalErr("failed to list the "+m.Ops.Plural(), err)
	}
	return list, nil
}

// GetRun reads run id of the database. Errors are *Error: KindNotFound, KindInternal.
func (m *RunManager) GetRun(ctx context.Context, id int64) (*evalsdb.TrainedModelRun, error) {
	run, err := m.Ops.Get(ctx, m.DB, id)
	if errors.Is(err, relational.ErrNotFound) {
		return nil, notFoundErr("unknown " + m.Ops.Noun())
	} else if err != nil {
		log.WithError(err).Errorf("training: read %s failed", m.Ops.Noun())
		return nil, internalErr("failed to read the "+m.Ops.Noun(), err)
	}
	return run, nil
}

// RunCases reads the cases stored so far of run id of the database, in the order of the eval or the dataset.
func (m *RunManager) RunCases(ctx context.Context, id int64) ([]evalsdb.TrainedModelCase, error) {
	cases, err := m.Ops.Cases(ctx, m.DB, id)
	if err != nil {
		log.WithError(err).Errorf("training: list %s cases failed", m.Ops.Noun())
		return nil, internalErr("failed to read the "+m.Ops.Noun()+"'s cases", err)
	}
	return cases, nil
}

// CancelRun stops a running run (it ends as "cancelled", keeping the batches done so far) and returns it. Errors are *Error:
// KindNotFound for an unknown run, KindConflict when it is not running.
func (m *RunManager) CancelRun(ctx context.Context, id int64) (*evalsdb.TrainedModelRun, error) {
	run, err := m.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if run.Status != evalsdb.TrainedModelRunRunning {
		return nil, conflictErr("the " + m.Ops.Noun() + " is not running")
	}
	m.mu.Lock()
	cur := m.running[run.TrainedModelID]
	m.mu.Unlock()
	if cur == nil || cur.runID != id {
		// Marked running, but nothing runs it (it cannot happen unless the bookkeeping failed): end it.
		if err := m.Ops.Finish(ctx, m.DB, id, evalsdb.TrainedModelRunCancelled, ""); err != nil {
			log.WithError(err).Errorf("training: ending the %s failed", m.Ops.Noun())
		}
	} else {
		cur.cancel()
	}
	return run, nil
}

// RunWatch is a run as a live feed starts: the run and its cases so far, then Events (nil if nothing is running the run any more:
// it is as stored).
type RunWatch struct {
	Run   *evalsdb.TrainedModelRun
	Cases []evalsdb.TrainedModelCase
	// Events are the events after the snapshot. The channel is closed when the feed ends or the watcher fell too far behind.
	Events chan RunEvent
	stream *RunStream
}

// Close ends the watch.
func (w *RunWatch) Close() {
	if w.Events != nil {
		w.stream.Unsubscribe(w.Events)
	}
}

// Watch starts watching run id of the database: the snapshot (the run and all of its cases so far: the judged and errored ones from the
// database, and the ones the model answered that the judge has not scored yet, with status "answered"), and the events that follow while
// the run goes on. The caller must Close it. Errors are *Error: KindNotFound for an unknown run, KindInternal.
func (m *RunManager) Watch(ctx context.Context, id int64) (*RunWatch, error) {
	// Subscribe before reading the database, so no event falls between the two; the overlap is harmless: the cases are
	// applied by idx, and the judged version wins.
	m.mu.Lock()
	stream := m.streams[id]
	m.mu.Unlock()
	w := &RunWatch{stream: stream}
	var pending []evalsdb.TrainedModelCase
	if stream != nil {
		pending, w.Events = stream.Subscribe()
	}
	fail := func(err error) (*RunWatch, error) { w.Close(); return nil, err }
	run, err := m.GetRun(ctx, id)
	if err != nil {
		return fail(err)
	}
	cases, err := m.RunCases(ctx, id)
	if err != nil {
		return fail(err)
	}
	stored := make(map[int]bool, len(cases))
	for _, it := range cases {
		stored[it.Idx] = true
	}
	for _, it := range pending {
		if !stored[it.Idx] {
			cases = append(cases, it)
		}
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Idx < cases[j].Idx })
	w.Run, w.Cases = run, cases
	return w, nil
}

// publishEnd announces the end of run runID on its stream, with the run as stored.
func (m *RunManager) publishEnd(ctx context.Context, s *RunStream, runID int64) {
	if run, err := m.Ops.Get(ctx, m.DB, runID); err == nil {
		s.End(*run)
	} else {
		log.WithError(err).WithField("run_id", runID).Warnf("training: read the ended %s for its stream failed", m.Ops.Noun())
		s.End(evalsdb.TrainedModelRun{ID: runID, Kind: m.Ops.Kind})
	}
}
