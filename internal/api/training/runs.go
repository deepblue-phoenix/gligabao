package training

// Runs of trained models judged by an LLM: the evals and the training data audits. Both are a TrainedModelRun that asks the model
// the question of each case and has the external LLM (the one used for knowledge ingestion) judge, a batch of question/answer
// pairs per LLM call, how close the answers are to the expected ones. They share everything but where the cases come from, so
// they share this file (the manager, the HTTP endpoints that follow a run, the background runner) and the stream (runs_stream.go):
//
//   - an EVAL (evals.go) asks the cases of an eval (bin/evals_training/*.json), which the model never saw in training: it
//     measures how well the model generalizes. Its cases have names and it can be part of an experiment;
//   - an AUDIT (audits.go) asks the examples of the model's OWN fine-tuning dataset: it checks that the training took, not how well
//     the model generalizes.
//
// The logic is the subsystem internal/evals/training; this file wires it to the sidecar (the model), the LLM (the judge) and the database
// (relational.TrainedModelRun, which holds the progress and the result), and runs it in the background: starting a run answers at
// once and the run is followed by reading its row (or its live feed).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	trainingeval "github.com/deepblue-phoenix/gligabao/internal/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
	log "github.com/sirupsen/logrus"
)

const (
	// CallEvalJudge and CallAuditJudge are the types of the runs (gb_inferences) the judge's LLM calls of an eval and of an audit
	// are recorded as; StageJudge is their stage.
	CallEvalJudge  = "training_eval_judge"
	CallAuditJudge = "training_audit_judge"
	StageJudge     = "judge"

	defaultRunMaxTokens = 1024
	// defaultRunTemperature is low: a run checks what the model learned, not how varied it can be.
	defaultRunTemperature = 0.1
	maxRunBody            = 1 << 14
	runsDefaultLimit      = 50
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

// RunOptions are the fields of the body of the request that starts a run, the same for an eval and an audit.
type RunOptions struct {
	Limit       int      `json:"limit"`
	BatchSize   int      `json:"batch_size"`
	JudgeModel  string   `json:"judge_model"`
	MaxTokens   int      `json:"max_tokens"`
	Temperature *float64 `json:"temperature"`
}

// resolve checks the options and returns the engine's config and the params to store; the error is safe to show.
func (o RunOptions) resolve() (trainingeval.Config, RunParams, error) {
	cfg, err := trainingeval.Config{BatchSize: o.BatchSize, JudgeModel: o.JudgeModel}.Normalize()
	switch {
	case err != nil:
		return cfg, RunParams{}, err
	case o.Limit < 0:
		return cfg, RunParams{}, errors.New("limit must be 0 (all) or more")
	case o.MaxTokens < 0 || o.MaxTokens > maxInferTokens:
		return cfg, RunParams{}, errors.New("max_tokens must be between 1 and " + strconv.Itoa(maxInferTokens))
	case o.Temperature != nil && (*o.Temperature <= 0 || *o.Temperature > 2):
		return cfg, RunParams{}, errors.New("temperature must be above 0 and at most 2")
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

// runOps is what differs between the two kinds of run in the database: the functions that store and read them.
type runOps struct {
	kind        string // relational.TrainedModelKindEval or ...KindAudit
	callType    string // the type of the judge's calls in gb_inferences
	record      func(ctx context.Context, d *sql.DB, r relational.TrainedModelRun) (int64, error)
	save        func(ctx context.Context, d *sql.DB, r relational.TrainedModelRun, cases []relational.TrainedModelCase) error
	finish      func(ctx context.Context, d *sql.DB, id int64, status, errText string) error
	failRunning func(ctx context.Context, d *sql.DB, reason string) (int64, error)
	get         func(ctx context.Context, d *sql.DB, id int64) (*relational.TrainedModelRun, error)
	list        func(ctx context.Context, d *sql.DB, modelID int64, limit int) ([]relational.TrainedModelRun, error)
	cases       func(ctx context.Context, d *sql.DB, runID int64) ([]relational.TrainedModelCase, error)
}

// noun is what the kind is called in messages ("eval" or "audit"); its URLs and the list in its responses are the plural.
func (o runOps) noun() string   { return o.kind }
func (o runOps) plural() string { return o.kind + "s" }

var evalOps = runOps{
	kind:        relational.TrainedModelKindEval,
	callType:    CallEvalJudge,
	record:      relational.RecordTrainedModelEvalRun,
	save:        relational.SaveTrainedModelEvalRunBatch,
	finish:      relational.FinishTrainedModelEvalRun,
	failRunning: relational.FailRunningTrainedModelEvalRuns,
	get:         relational.GetTrainedModelEvalRun,
	list:        relational.ListTrainedModelEvalRuns,
	cases:       relational.ListTrainedModelEvalCases,
}

var auditOps = runOps{
	kind:        relational.TrainedModelKindAudit,
	callType:    CallAuditJudge,
	record:      relational.RecordTrainedModelAuditRun,
	save:        relational.SaveTrainedModelAuditRunBatch,
	finish:      relational.FinishTrainedModelAuditRun,
	failRunning: relational.FailRunningTrainedModelAuditRuns,
	get:         relational.GetTrainedModelAuditRun,
	list:        relational.ListTrainedModelAuditRuns,
	cases:       relational.ListTrainedModelAuditCases,
}

// runManager starts the runs of one kind and runs them in the background, one at a time per trained model. Runs still running when
// the server last stopped are marked error when the manager is made. EvalManager and AuditManager are one each.
type runManager struct {
	ops runOps
	ctx context.Context
	dbs []Database
	sc  Inferer
	llm memory_core.LLM

	mu      sync.Mutex
	running map[runKey]*runningRun
	streams map[streamKey]*runStream // the live feeds (runs_stream.go) of the runs that run
	wg      sync.WaitGroup
}

type runKey struct {
	db      string
	modelID int64
}

type runningRun struct {
	runID  int64 // 0 until the run's row is made
	cancel context.CancelFunc
}

// newRunManager makes the manager of the runs of the trained models of dbs. sc answers the questions (nil: starting a run answers
// 503) and llm judges (nil: the same). The runs stop when ctx is done. Runs that a restart interrupted (still "running" in the
// database) are marked error.
func newRunManager(ctx context.Context, dbs []Database, sc Inferer, llm memory_core.LLM, ops runOps) *runManager {
	m := &runManager{ops: ops, ctx: ctx, dbs: dbs, sc: sc, llm: llm, running: map[runKey]*runningRun{}, streams: map[streamKey]*runStream{}}
	for _, n := range dbs {
		if k, err := ops.failRunning(ctx, n.DB, "interrupted: the server restarted while the "+ops.noun()+" ran"); err != nil {
			log.WithError(err).WithField("db", n.Name).Warnf("training: could not mark interrupted %ss as error", ops.noun())
		} else if k > 0 {
			log.WithField("db", n.Name).Infof("training: %d %s(s) were interrupted by a restart and marked error", k, ops.noun())
		}
	}
	return m
}

// Wait blocks until the running runs have ended (after the manager's context is done, or for tests).
func (m *runManager) Wait() { m.wg.Wait() }

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		httputil.Error(w, http.StatusBadRequest, "id must be a positive integer")
		return 0, false
	}
	return id, true
}

// startPrelude is the start of every StartOf: the database, the model's ID, and a sidecar and an LLM to ask and judge with.
func (m *runManager) startPrelude(w http.ResponseWriter, r *http.Request) (Database, int64, bool) {
	n, ok := pick(m.dbs, r.URL.Query().Get("db"))
	if !ok {
		httputil.Error(w, http.StatusNotFound, "unknown database")
		return n, 0, false
	}
	id, ok := pathID(w, r)
	if !ok {
		return n, 0, false
	}
	if m.sc == nil {
		httputil.Error(w, http.StatusServiceUnavailable, "sidecar is not active")
		return n, 0, false
	}
	if m.llm == nil {
		httputil.Error(w, http.StatusServiceUnavailable, "no LLM is available to judge the answers (set ANTHROPIC_API_KEY)")
		return n, 0, false
	}
	return n, id, true
}

// getModel reads trained model id, answering 404 or 500 itself.
func getModel(w http.ResponseWriter, r *http.Request, n Database, id int64) (*relational.TrainedModel, bool) {
	tm, err := relational.GetTrainedModel(r.Context(), n.DB, id)
	if errors.Is(err, relational.ErrNotFound) {
		httputil.Error(w, http.StatusNotFound, "unknown trained model")
		return nil, false
	} else if err != nil {
		log.WithError(err).Error("training: read trained model failed")
		httputil.Error(w, http.StatusInternalServerError, "failed to read the trained model")
		return nil, false
	}
	return tm, true
}

// runSpec is a run ready to start: what StartOf of each kind works out and start carries out.
type runSpec struct {
	n      Database
	tm     *relational.TrainedModel
	cfg    trainingeval.Config
	params RunParams
	// row is the run to record (Eval, or Dataset and DatasetSHA256; the settings and NumExamples are set by start).
	row relational.TrainedModelRun
	// examples are the questions to ask and names the names of the cases that go with them (nil for an audit's).
	examples []trainingeval.Example
	names    []string
	// afterRecord, if set, is called with the ID of the recorded run, before it starts (an eval joins its experiment there).
	afterRecord func(ctx context.Context, id int64) error
}

// start records the run and starts it in the background, answering 202 with it: 409 if the model has a run of this kind
// already, 500 if it could not be recorded.
func (m *runManager) start(w http.ResponseWriter, r *http.Request, s runSpec) {
	noun, n, tm := m.ops.noun(), s.n, s.tm
	// One run at a time per model and kind: it would otherwise compete with itself for the model's GPU container.
	key := runKey{n.Name, tm.ID}
	runCtx, cancel := context.WithCancel(m.ctx)
	run := &runningRun{cancel: cancel}
	m.mu.Lock()
	if _, busy := m.running[key]; busy {
		m.mu.Unlock()
		cancel()
		httputil.Error(w, http.StatusConflict, "an "+noun+" of this model is running already")
		return
	}
	m.running[key] = run
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		delete(m.running, key)
		m.mu.Unlock()
		cancel()
	}

	pj, _ := json.Marshal(s.params)
	dbCtx := context.WithoutCancel(r.Context())
	row := s.row
	row.TrainedModelID, row.Params, row.JudgeModel, row.NumExamples = tm.ID, pj, s.cfg.JudgeModel, len(s.examples)
	runID, err := m.ops.record(dbCtx, n.DB, row)
	if err != nil {
		release()
		log.WithError(err).WithField("model_id", tm.ID).Errorf("training: recording the %s failed", noun)
		httputil.Error(w, http.StatusInternalServerError, "failed to record the "+noun)
		return
	}
	fail := func(msg string, err error) {
		release()
		_ = m.ops.finish(dbCtx, n.DB, runID, relational.TrainedModelRunErrored, "could not start")
		log.WithError(err).WithField("run_id", runID).Errorf("training: %s", msg)
		httputil.Error(w, http.StatusInternalServerError, "failed to "+msg)
	}
	if s.afterRecord != nil {
		if err := s.afterRecord(dbCtx, runID); err != nil {
			fail("add the "+noun+" to the experiment", err)
			return
		}
	}
	stream := newRunStream()
	skey := streamKey{n.Name, runID}
	m.mu.Lock()
	run.runID = runID
	m.streams[skey] = stream
	m.mu.Unlock()
	rec, err := m.ops.get(dbCtx, n.DB, runID)
	if err != nil {
		m.dropStream(skey, stream)
		fail("read the "+noun, err)
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer release()
		defer m.dropStream(skey, stream)
		m.run(runCtx, s, runID, stream)
	}()
	httputil.WriteJSON(w, http.StatusAccepted, rec)
}

// RunProgress is the run's row with the counters of the engine's progress: what SaveTrainedModel*RunBatch writes after a batch.
func RunProgress(runID int64, p trainingeval.Progress) relational.TrainedModelRun {
	return relational.TrainedModelRun{ID: runID, JudgeModel: p.JudgeModel, NumAnswered: p.Answered, NumJudged: p.Judged, NumErrored: p.Errored,
		NumCorrect: p.Correct, NumPartial: p.Partial, NumIncorrect: p.Incorrect, ScoreSum: p.ScoreSum, NumBatches: p.Batches,
		JudgeCalls: p.JudgeCalls, JudgeInputTokens: p.JudgeInputTokens, JudgeOutputTokens: p.JudgeOutputTokens, JudgeCostUSD: p.JudgeCostUSD}
}

// Generator makes the engine's GenerateFunc of trained model tm: one sidecar inference per question, with the settings of params
// (the server's runs and the command-line runner internal/evals/training/cmd ask the model the same way).
func Generator(sc Inferer, tm *relational.TrainedModel, params RunParams) trainingeval.GenerateFunc {
	return func(ctx context.Context, prompt string) (trainingeval.Generation, error) {
		ctx, cancel := context.WithTimeout(ctx, inferTimeout)
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
func CaseRow(runID int64, names []string, it trainingeval.CaseResult) relational.TrainedModelCase {
	c := relational.TrainedModelCase{RunID: runID, Idx: it.Idx, Batch: it.Batch, Instruction: it.Instruction, Input: it.Input,
		Reference: it.Reference, Response: it.Response, Status: it.Status, Error: it.Error, Score: it.Score, Reason: it.Reason,
		InputTokens: it.InputTokens, OutputTokens: it.OutputTokens, DurationMS: it.DurationMS}
	if it.Idx >= 0 && it.Idx < len(names) {
		c.Name = names[it.Idx]
	}
	return c
}

// run is the run's goroutine: it runs trainingeval.Run, storing every finished batch, and ends the run's row.
func (m *runManager) run(ctx context.Context, s runSpec, runID int64, stream *runStream) {
	noun, n, tm, params := m.ops.noun(), s.n, s.tm, s.params
	dbCtx := context.WithoutCancel(ctx) // the writes must outlive a cancelled run
	logf := log.WithFields(log.Fields{"db": n.Name, "model_id": tm.ID, "run_id": runID, "kind": noun})
	logf.WithField("examples", len(s.examples)).Infof("training: %s started", noun)
	finish := func(status, errText string) {
		if err := m.ops.finish(dbCtx, n.DB, runID, status, errText); err != nil {
			logf.WithError(err).Errorf("training: ending the %s failed", noun)
		}
		logf.WithField("status", status).Infof("training: %s ended", noun)
		m.publishEnd(dbCtx, n, stream, runID)
	}
	defer func() {
		if r := recover(); r != nil {
			logf.Errorf("training: %s panicked: %v", noun, r)
			finish(relational.TrainedModelRunErrored, fmt.Sprintf("internal error: %v", r))
		}
	}()

	gen := Generator(m.sc, tm, params)
	judge := memory_core.WithRecording(m.llm, n.DB, m.ops.callType)
	sink := func(_ context.Context, cases []trainingeval.CaseResult, p trainingeval.Progress) error {
		rows := make([]relational.TrainedModelCase, len(cases))
		for i, it := range cases {
			rows[i] = CaseRow(runID, s.names, it)
		}
		rec := RunProgress(runID, p)
		if err := m.ops.save(dbCtx, n.DB, rec, rows); err != nil {
			return err
		}
		if full, err := m.ops.get(dbCtx, n.DB, runID); err == nil { // the whole row, for the live feed
			rec = *full
		}
		stream.batch(rec, rows)
		return nil
	}
	cfg := s.cfg
	cfg.OnAnswer = func(it trainingeval.CaseResult) { stream.answer(CaseRow(runID, s.names, it)) }
	_, err := trainingeval.Run(memory_core.WithStage(ctx, StageJudge), cfg, s.examples, gen, judge, sink)
	switch {
	case err == nil:
		finish(relational.TrainedModelRunCompleted, "")
	case errors.Is(err, context.Canceled) && m.ctx.Err() != nil:
		finish(relational.TrainedModelRunErrored, "interrupted: the server shut down while the "+noun+" ran")
	case errors.Is(err, context.Canceled):
		finish(relational.TrainedModelRunCancelled, "")
	default:
		logf.WithError(err).Warnf("training: %s failed", noun)
		finish(relational.TrainedModelRunErrored, err.Error())
	}
}

// dropStream forgets the live feed of a run that is over.
func (m *runManager) dropStream(key streamKey, s *runStream) {
	m.mu.Lock()
	if m.streams[key] == s {
		delete(m.streams, key)
	}
	m.mu.Unlock()
}

// ListOf serves GET /v1/gb/training/models/{id}/evals|audits[?db=<name>][&limit=<n>]: {"evals": [...]} or {"audits": [...]}, the
// runs of trained model {id} (relational.TrainedModelRun, without their cases), newest first, at most limit (default 50, at most
// 500). 404 for an unknown model.
func (m *runManager) ListOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, ok := pick(m.dbs, q.Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		limit := runsDefaultLimit
		if s := q.Get("limit"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil || v < 1 {
				httputil.Error(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			limit = min(v, trainedModelsMaxLimit)
		}
		if _, ok := getModel(w, r, n, id); !ok {
			return
		}
		list, err := m.ops.list(r.Context(), n.DB, id, limit)
		if err != nil {
			log.WithError(err).Errorf("training: list %ss failed", m.ops.noun())
			httputil.Error(w, http.StatusInternalServerError, "failed to list the "+m.ops.plural())
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{m.ops.plural(): list})
	}
}

// readRun reads run id, answering 404 or 500 itself.
func (m *runManager) readRun(w http.ResponseWriter, r *http.Request, n Database, id int64) (*relational.TrainedModelRun, bool) {
	run, err := m.ops.get(r.Context(), n.DB, id)
	if errors.Is(err, relational.ErrNotFound) {
		httputil.Error(w, http.StatusNotFound, "unknown "+m.ops.noun())
		return nil, false
	} else if err != nil {
		log.WithError(err).Errorf("training: read %s failed", m.ops.noun())
		httputil.Error(w, http.StatusInternalServerError, "failed to read the "+m.ops.noun())
		return nil, false
	}
	return run, true
}

// GetOf serves GET /v1/gb/training/evals|audits/{id}[?db=<name>]: {"run": ..., "cases": [...]}, one run with the cases stored so far
// (relational.TrainedModelCase: question, the expected answer, the model's, score and reason), in the order of the eval or the
// dataset. 404 for an unknown run.
func (m *runManager) GetOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(m.dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, ok := m.readRun(w, r, n, id)
		if !ok {
			return
		}
		cases, err := m.ops.cases(r.Context(), n.DB, id)
		if err != nil {
			log.WithError(err).Errorf("training: list %s cases failed", m.ops.noun())
			httputil.Error(w, http.StatusInternalServerError, "failed to read the "+m.ops.noun()+"'s cases")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"run": run, "cases": cases})
	}
}

// CancelOf serves POST /v1/gb/training/evals|audits/{id}/cancel[?db=<name>]: stops a running run (it ends as "cancelled", keeping
// the batches done so far) and answers 202 with it. 404 for an unknown run, 409 when it is not running.
func (m *runManager) CancelOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(m.dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, ok := m.readRun(w, r, n, id)
		if !ok {
			return
		}
		if run.Status != relational.TrainedModelRunRunning {
			httputil.Error(w, http.StatusConflict, "the "+m.ops.noun()+" is not running")
			return
		}
		m.mu.Lock()
		cur := m.running[runKey{n.Name, run.TrainedModelID}]
		m.mu.Unlock()
		if cur == nil || cur.runID != id {
			// Marked running, but nothing runs it (it cannot happen unless the bookkeeping failed): end it.
			if err := m.ops.finish(r.Context(), n.DB, id, relational.TrainedModelRunCancelled, ""); err != nil {
				log.WithError(err).Errorf("training: ending the %s failed", m.ops.noun())
			}
		} else {
			cur.cancel()
		}
		httputil.WriteJSON(w, http.StatusAccepted, run)
	}
}
