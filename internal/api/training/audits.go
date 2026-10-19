package training

// Training data audits of trained models: the "Audit" button of the admin UI. An audit run is one run of a trained model against its
// OWN fine-tuning dataset (relational.TrainedModelAuditRun): it asks the model the question of every example (a case) and has the
// external LLM (the one used for knowledge ingestion) judge, a batch of question/answer pairs per LLM call, how closely the answer
// reproduces the dataset's output. Every case is something the model was trained on, so the score says whether the training took
// (and, through the examples it fails, which training data is bad); it is NOT an eval: it does not measure how well the model
// generalizes. The evals of trained models (evals.go) are the separate thing, for cases the model never saw.
//
// The logic is the subsystem evals/training (shared with the evals); this file wires it to the sidecar (the model), the LLM (the
// judge) and the database (relational.TrainedModelAuditRun, which holds the progress and the result), and runs it in the
// background: starting an audit answers at once and the run is followed by reading its row.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	trainingeval "github.com/deepblue-phoenix/gligabao/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
	log "github.com/sirupsen/logrus"
)

const (
	// CallAuditJudge is the type of the runs (gb_inferences) the audit judge's LLM calls are recorded as (the evals' is CallEvalJudge).
	CallAuditJudge = "training_audit_judge"
	// StageAuditJudge is their stage.
	StageAuditJudge = "judge"

	defaultAuditMaxTokens = 1024
	// defaultAuditTemperature is low: the audit checks what the model learned, not how varied it can be.
	defaultAuditTemperature = 0.1
	maxAuditBody            = 1 << 14
	auditsDefaultLimit      = 50
)

// AuditParams are the settings an audit was started with (the "params" of its row).
type AuditParams struct {
	BatchSize   int     `json:"batch_size"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
	// Limit is the number of questions asked: the first ones of the dataset (0 = all of them).
	Limit int `json:"limit"`
}

// AuditManager starts the training data audits and runs them in the background, one at a time per trained model. Audits still
// running when the server last stopped are marked failed when the manager is made.
type AuditManager struct {
	ctx context.Context
	dbs []Database
	sc  Inferer
	llm memory_core.LLM

	mu      sync.Mutex
	running map[auditKey]*runningAudit
	streams map[auditStreamKey]*auditStream // the live feeds (audits_stream.go) of the audits that run
	wg      sync.WaitGroup
}

type auditKey struct {
	db      string
	modelID int64
}

type runningAudit struct {
	auditID int64 // 0 until the audit row is made
	cancel  context.CancelFunc
}

// NewAuditManager makes the manager of the training data audits of the trained models of dbs. sc answers the questions (nil:
// starting an audit answers 503) and llm judges (nil: the same). The audits stop when ctx is done. Audits that a restart
// interrupted (still "running" in the database) are marked failed.
func NewAuditManager(ctx context.Context, dbs []Database, sc Inferer, llm memory_core.LLM) *AuditManager {
	m := &AuditManager{ctx: ctx, dbs: dbs, sc: sc, llm: llm, running: map[auditKey]*runningAudit{}, streams: map[auditStreamKey]*auditStream{}}
	for _, n := range dbs {
		if k, err := relational.FailRunningTrainedModelAuditRuns(ctx, n.DB, "interrupted: the server restarted while the audit ran"); err != nil {
			log.WithError(err).WithField("db", n.Name).Warn("training: could not mark interrupted audits as failed")
		} else if k > 0 {
			log.WithField("db", n.Name).Infof("training: %d audit(s) were interrupted by a restart and marked failed", k)
		}
	}
	return m
}

// Wait blocks until the running audits have ended (after the manager's context is done, or for tests).
func (m *AuditManager) Wait() { m.wg.Wait() }

// StartOf serves POST /v1/gb/training/models/{id}/audits[?db=<name>] with an optional JSON body {"limit": 0, "batch_size": 10,
// "judge_model": "", "max_tokens": 1024, "temperature": 0.1}: it starts a training data audit of trained model {id} and answers 202
// with the audit (relational.TrainedModelAuditRun, status "running"); the work goes on in the background (see GetOf, ListOf, CancelOf).
//   - limit: ask only the first n cases (default: all of them)
//   - batch_size: question/answer pairs per judge call (default 10, at most 25)
//   - judge_model: the LLM model that judges (default: the server's default model)
//   - max_tokens, temperature: of the model's answers (default 1024 and 0.1)
//
// 404 for an unknown model, 400 for a bad body or when the model's dataset cannot be read (none recorded, gone from the training
// datasets directory, invalid), 409 when an audit of this model is running already, 503 without a sidecar or an LLM.
func (m *AuditManager) StartOf() http.HandlerFunc {
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
		if m.sc == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "sidecar is not active")
			return
		}
		if m.llm == nil {
			httputil.Error(w, http.StatusServiceUnavailable, "no LLM is available to judge the answers (set ANTHROPIC_API_KEY)")
			return
		}
		var in struct {
			Limit       int      `json:"limit"`
			BatchSize   int      `json:"batch_size"`
			JudgeModel  string   `json:"judge_model"`
			MaxTokens   int      `json:"max_tokens"`
			Temperature *float64 `json:"temperature"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuditBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		cfg, err := trainingeval.Config{BatchSize: in.BatchSize, JudgeModel: in.JudgeModel}.Normalize()
		switch {
		case err != nil:
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		case in.Limit < 0:
			httputil.Error(w, http.StatusBadRequest, "limit must be 0 (all) or more")
			return
		case in.MaxTokens < 0 || in.MaxTokens > maxInferTokens:
			httputil.Error(w, http.StatusBadRequest, "max_tokens must be between 1 and "+strconv.Itoa(maxInferTokens))
			return
		case in.Temperature != nil && (*in.Temperature <= 0 || *in.Temperature > 2):
			httputil.Error(w, http.StatusBadRequest, "temperature must be above 0 and at most 2")
			return
		}
		params := AuditParams{BatchSize: cfg.BatchSize, MaxTokens: in.MaxTokens, Temperature: defaultAuditTemperature, Limit: in.Limit}
		if params.MaxTokens == 0 {
			params.MaxTokens = defaultAuditMaxTokens
		}
		if in.Temperature != nil {
			params.Temperature = *in.Temperature
		}

		tm, err := relational.GetTrainedModel(r.Context(), n.DB, id)
		if errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown trained model")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read trained model failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the trained model")
			return
		}
		dataset, sum, examples, err := loadModelDataset(n, tm)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if params.Limit > 0 && params.Limit < len(examples) {
			examples = examples[:params.Limit]
		}

		// One audit at a time per model: it would otherwise compete with itself for the model's GPU container.
		key := auditKey{n.Name, tm.ID}
		runCtx, cancel := context.WithCancel(m.ctx)
		run := &runningAudit{cancel: cancel}
		m.mu.Lock()
		if _, busy := m.running[key]; busy {
			m.mu.Unlock()
			cancel()
			httputil.Error(w, http.StatusConflict, "an audit of this model is running already")
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

		pj, _ := json.Marshal(params)
		dbCtx := context.WithoutCancel(r.Context())
		auditID, err := relational.RecordTrainedModelAuditRun(dbCtx, n.DB, relational.TrainedModelAuditRun{
			TrainedModelID: tm.ID, Dataset: dataset, DatasetSHA256: sum, Params: pj, JudgeModel: cfg.JudgeModel, NumExamples: len(examples)})
		if err != nil {
			release()
			log.WithError(err).WithField("model_id", tm.ID).Error("training: recording the audit failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to record the audit")
			return
		}
		stream := newAuditStream()
		skey := auditStreamKey{n.Name, auditID}
		m.mu.Lock()
		run.auditID = auditID
		m.streams[skey] = stream
		m.mu.Unlock()
		au, err := relational.GetTrainedModelAuditRun(dbCtx, n.DB, auditID)
		if err != nil {
			release()
			m.dropStream(skey, stream)
			_ = relational.FinishTrainedModelAuditRun(dbCtx, n.DB, auditID, relational.TrainedModelRunFailed, "could not start")
			log.WithError(err).Error("training: read audit failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the audit")
			return
		}
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			defer release()
			defer m.dropStream(skey, stream)
			m.run(runCtx, n, *tm, auditID, stream, examples, cfg, params)
		}()
		httputil.WriteJSON(w, http.StatusAccepted, au)
	}
}

// run is the audit's goroutine: it runs trainingeval.Run, storing every finished batch, and ends the audit's row.
func (m *AuditManager) run(ctx context.Context, n Database, tm relational.TrainedModel, auditID int64, stream *auditStream, examples []trainingeval.Example, cfg trainingeval.Config, params AuditParams) {
	dbCtx := context.WithoutCancel(ctx) // the writes must outlive a cancelled audit
	logf := log.WithFields(log.Fields{"db": n.Name, "model_id": tm.ID, "audit_run_id": auditID})
	logf.WithField("examples", len(examples)).Info("training: audit started")
	finish := func(status, errText string) {
		if err := relational.FinishTrainedModelAuditRun(dbCtx, n.DB, auditID, status, errText); err != nil {
			logf.WithError(err).Error("training: ending the audit failed")
		}
		logf.WithField("status", status).Info("training: audit ended")
		m.publishEnd(dbCtx, n, stream, auditID)
	}
	defer func() {
		if r := recover(); r != nil {
			logf.Errorf("training: audit panicked: %v", r)
			finish(relational.TrainedModelRunFailed, fmt.Sprintf("internal error: %v", r))
		}
	}()

	gen := func(ctx context.Context, prompt string) (trainingeval.Generation, error) {
		ctx, cancel := context.WithTimeout(ctx, inferTimeout)
		defer cancel()
		out, err := m.sc.Infer(ctx, sidecar.InferRequest{Prompt: prompt, ModelSize: tm.ModelSize, ModelPath: tm.Path,
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
	judge := memory_core.WithRecording(m.llm, n.DB, CallAuditJudge)
	sink := func(_ context.Context, cases []trainingeval.CaseResult, p trainingeval.Progress) error {
		rows := make([]relational.TrainedModelAuditCase, len(cases))
		for i, it := range cases {
			rows[i] = auditCaseRow(auditID, it)
		}
		a := relational.TrainedModelAuditRun{ID: auditID, JudgeModel: p.JudgeModel, NumAnswered: p.Answered, NumJudged: p.Judged, NumFailed: p.Failed,
			NumCorrect: p.Correct, NumPartial: p.Partial, NumIncorrect: p.Incorrect, ScoreSum: p.ScoreSum, NumBatches: p.Batches,
			JudgeCalls: p.JudgeCalls, JudgeInputTokens: p.JudgeInputTokens, JudgeOutputTokens: p.JudgeOutputTokens, JudgeCostUSD: p.JudgeCostUSD}
		if err := relational.SaveTrainedModelAuditRunBatch(dbCtx, n.DB, a, rows); err != nil {
			return err
		}
		if full, err := relational.GetTrainedModelAuditRun(dbCtx, n.DB, auditID); err == nil { // the whole row, for the live feed
			a = *full
		}
		stream.batch(a, rows)
		return nil
	}
	cfg.OnAnswer = func(it trainingeval.CaseResult) { stream.answer(auditCaseRow(auditID, it)) }
	_, err := trainingeval.Run(memory_core.WithStage(ctx, StageAuditJudge), cfg, examples, gen, judge, sink)
	switch {
	case err == nil:
		finish(relational.TrainedModelRunCompleted, "")
	case errors.Is(err, context.Canceled) && m.ctx.Err() != nil:
		finish(relational.TrainedModelRunFailed, "interrupted: the server shut down while the audit ran")
	case errors.Is(err, context.Canceled):
		finish(relational.TrainedModelRunCancelled, "")
	default:
		logf.WithError(err).Warn("training: audit failed")
		finish(relational.TrainedModelRunFailed, err.Error())
	}
}

// dropStream forgets the live feed of an audit that is over.
func (m *AuditManager) dropStream(key auditStreamKey, s *auditStream) {
	m.mu.Lock()
	if m.streams[key] == s {
		delete(m.streams, key)
	}
	m.mu.Unlock()
}

// ListOf serves GET /v1/gb/training/models/{id}/audits[?db=<name>][&limit=<n>]: {"audits": [...]}, the audits of trained model {id}
// (relational.TrainedModelAuditRun, without their cases), newest first, at most limit (default 50, at most 500). 404 for an unknown model.
func (m *AuditManager) ListOf() http.HandlerFunc {
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
		limit := auditsDefaultLimit
		if s := q.Get("limit"); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil || v < 1 {
				httputil.Error(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			limit = min(v, trainedModelsMaxLimit)
		}
		if _, err := relational.GetTrainedModel(r.Context(), n.DB, id); errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown trained model")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read trained model failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the trained model")
			return
		}
		list, err := relational.ListTrainedModelAuditRuns(r.Context(), n.DB, id, limit)
		if err != nil {
			log.WithError(err).Error("training: list audits failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the audits")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"audits": list})
	}
}

// GetOf serves GET /v1/gb/training/audits/{id}[?db=<name>]: {"audit": ..., "cases": [...]}, one audit with the cases stored so far
// (relational.TrainedModelAuditCase: question, the dataset's answer, the model's, score and reason), in dataset order. 404 for an
// unknown audit.
func (m *AuditManager) GetOf() http.HandlerFunc {
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
		au, err := relational.GetTrainedModelAuditRun(r.Context(), n.DB, id)
		if errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown audit")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read audit failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the audit")
			return
		}
		cases, err := relational.ListTrainedModelAuditCases(r.Context(), n.DB, id)
		if err != nil {
			log.WithError(err).Error("training: list audit cases failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the audit's cases")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"audit": au, "cases": cases})
	}
}

// CancelOf serves POST /v1/gb/training/audits/{id}/cancel[?db=<name>]: stops a running audit (it ends as "cancelled", keeping the
// batches done so far) and answers 202 with the audit. 404 for an unknown audit, 409 when it is not running.
func (m *AuditManager) CancelOf() http.HandlerFunc {
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
		au, err := relational.GetTrainedModelAuditRun(r.Context(), n.DB, id)
		if errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown audit")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read audit failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the audit")
			return
		}
		if au.Status != relational.TrainedModelRunRunning {
			httputil.Error(w, http.StatusConflict, "the audit is not running")
			return
		}
		m.mu.Lock()
		run := m.running[auditKey{n.Name, au.TrainedModelID}]
		m.mu.Unlock()
		if run == nil || run.auditID != id {
			// Marked running, but nothing runs it (it cannot happen unless the bookkeeping failed): end it.
			if err := relational.FinishTrainedModelAuditRun(r.Context(), n.DB, id, relational.TrainedModelRunCancelled, ""); err != nil {
				log.WithError(err).Error("training: ending the audit failed")
			}
		} else {
			run.cancel()
		}
		httputil.WriteJSON(w, http.StatusAccepted, au)
	}
}
