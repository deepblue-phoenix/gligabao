package training

// Evals of trained models: the "eval" button of the admin UI. An eval asks a trained model the questions of its training
// dataset and has the external LLM (the one used for knowledge ingestion) judge, a batch of question/answer pairs per LLM call,
// how close the answers are to the dataset's. The logic is the eval subsystem evals/training; this file wires it to the
// sidecar (the model), the LLM (the judge) and the database (relational.TrainedModelEval, which holds the progress and the
// result), and runs it in the background: starting an eval answers at once and the eval is followed by reading its row.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	trainingeval "github.com/deepblue-phoenix/gligabao/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
	log "github.com/sirupsen/logrus"
)

const (
	// CallEvalJudge is the type of the runs (gb_inferences) the judge's LLM calls are recorded as.
	CallEvalJudge = "eval_judge"
	// StageEvalJudge is their stage.
	StageEvalJudge = "judge"

	defaultEvalMaxTokens = 1024
	// defaultEvalTemperature is low: the eval checks what the model learned, not how varied it can be.
	defaultEvalTemperature = 0.1
	maxEvalBody            = 1 << 14
	evalsDefaultLimit      = 50
)

// EvalParams are the settings an eval was started with (the "params" of its row).
type EvalParams struct {
	BatchSize   int     `json:"batch_size"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
	// Limit is the number of questions asked: the first ones of the dataset (0 = all of them).
	Limit int `json:"limit"`
}

// EvalManager starts the evals and runs them in the background, one at a time per trained model. Evals still running
// when the server last stopped are marked failed when the manager is made.
type EvalManager struct {
	ctx context.Context
	dbs []Database
	sc  Inferer
	llm memory_core.LLM

	mu      sync.Mutex
	running map[evalKey]*runningEval
	wg      sync.WaitGroup
}

type evalKey struct {
	db      string
	modelID int64
}

type runningEval struct {
	evalID int64 // 0 until the eval row is made
	cancel context.CancelFunc
}

// NewEvalManager makes the manager of the evals of the trained models of dbs. sc answers the questions (nil: starting an eval
// answers 503) and llm judges (nil: the same). The evals stop when ctx is done. Evals that a restart interrupted (still
// "running" in the database) are marked failed.
func NewEvalManager(ctx context.Context, dbs []Database, sc Inferer, llm memory_core.LLM) *EvalManager {
	m := &EvalManager{ctx: ctx, dbs: dbs, sc: sc, llm: llm, running: map[evalKey]*runningEval{}}
	for _, n := range dbs {
		if k, err := relational.FailRunningTrainedModelEvals(ctx, n.DB, "interrupted: the server restarted while the eval ran"); err != nil {
			log.WithError(err).WithField("db", n.Name).Warn("training: could not mark interrupted evals as failed")
		} else if k > 0 {
			log.WithField("db", n.Name).Infof("training: %d eval(s) were interrupted by a restart and marked failed", k)
		}
	}
	return m
}

// Wait blocks until the running evals have ended (after the manager's context is done, or for tests).
func (m *EvalManager) Wait() { m.wg.Wait() }

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		httputil.Error(w, http.StatusBadRequest, "id must be a positive integer")
		return 0, false
	}
	return id, true
}

// StartOf serves POST /v1/gb/training/models/{id}/evals[?db=<name>] with an optional JSON body {"limit": 0, "batch_size": 10,
// "judge_model": "", "max_tokens": 1024, "temperature": 0.1}: it starts an eval of trained model {id}
// on its training dataset (the questions are the dataset's instructions, the references its outputs) and answers 202 with the
// eval (relational.TrainedModelEval, status "running"); the work goes on in the background (see GetOf, ListOf, CancelOf).
//   - limit: ask only the first n questions (default: all of them)
//   - batch_size: question/answer pairs per judge call (default 10, at most 25)
//   - judge_model: the LLM model that judges (default: the server's default model)
//   - max_tokens, temperature: of the model's answers (default 1024 and 0.1)
//
// 404 for an unknown model, 400 for a bad body or when the model's dataset cannot be read (none recorded, gone from the
// training datasets directory, invalid), 409 when an eval of this model is running already, 503 without a sidecar or an LLM.
func (m *EvalManager) StartOf() http.HandlerFunc {
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
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEvalBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
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
		params := EvalParams{BatchSize: cfg.BatchSize, MaxTokens: in.MaxTokens, Temperature: defaultEvalTemperature, Limit: in.Limit}
		if params.MaxTokens == 0 {
			params.MaxTokens = defaultEvalMaxTokens
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
		dataset, sum, examples, err := loadEvalDataset(n, tm)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if params.Limit > 0 && params.Limit < len(examples) {
			examples = examples[:params.Limit]
		}

		// One eval at a time per model: it would otherwise compete with itself for the model's GPU container.
		key := evalKey{n.Name, tm.ID}
		runCtx, cancel := context.WithCancel(m.ctx)
		run := &runningEval{cancel: cancel}
		m.mu.Lock()
		if _, busy := m.running[key]; busy {
			m.mu.Unlock()
			cancel()
			httputil.Error(w, http.StatusConflict, "an eval of this model is running already")
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
		evalID, err := relational.RecordTrainedModelEval(dbCtx, n.DB, relational.TrainedModelEval{
			TrainedModelID: tm.ID, Dataset: dataset, DatasetSHA256: sum, Params: pj, JudgeModel: cfg.JudgeModel, NumExamples: len(examples)})
		if err != nil {
			release()
			log.WithError(err).WithField("model_id", tm.ID).Error("training: recording the eval failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to record the eval")
			return
		}
		m.mu.Lock()
		run.evalID = evalID
		m.mu.Unlock()
		ev, err := relational.GetTrainedModelEval(dbCtx, n.DB, evalID)
		if err != nil {
			release()
			_ = relational.FinishTrainedModelEval(dbCtx, n.DB, evalID, relational.TrainedModelEvalFailed, "could not start")
			log.WithError(err).Error("training: read eval failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the eval")
			return
		}
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			defer release()
			m.run(runCtx, n, *tm, evalID, examples, cfg, params)
		}()
		httputil.WriteJSON(w, http.StatusAccepted, ev)
	}
}

// loadEvalDataset reads the training dataset of tm from n's training datasets directory: the dataset's name, the digest of
// the file and its examples. The errors are safe to show to the user.
func loadEvalDataset(n Database, tm *relational.TrainedModel) (name, sum string, examples []trainingeval.Example, err error) {
	name = strings.TrimSuffix(tm.Dataset, ".jsonl")
	if name == "" {
		return "", "", nil, errors.New("this model was trained on the sidecar's default dataset, which gligabao does not have: there are no questions to ask")
	}
	ds, err := findDataset(n, name)
	if err != nil {
		return "", "", nil, fmt.Errorf("the training dataset %s of this model is not in the training datasets directory", name)
	}
	content, err := os.ReadFile(ds.Path)
	if err != nil {
		log.WithError(err).WithField("dataset", name).Error("training: read training dataset file failed")
		return "", "", nil, fmt.Errorf("the data file of dataset %s cannot be read", name)
	}
	if len(content) > MaxDatasetBytes {
		return "", "", nil, fmt.Errorf("the data file of dataset %s is over %d MB", name, MaxDatasetBytes>>20)
	}
	if examples, err = trainingeval.ParseExamples(content); err != nil {
		return "", "", nil, fmt.Errorf("dataset %s: %w", name, err)
	}
	h := sha256.Sum256(content)
	return name, hex.EncodeToString(h[:]), examples, nil
}

// run is the eval's goroutine: it runs trainingeval.Run, storing every finished batch, and ends the eval's row.
func (m *EvalManager) run(ctx context.Context, n Database, tm relational.TrainedModel, evalID int64, examples []trainingeval.Example, cfg trainingeval.Config, params EvalParams) {
	dbCtx := context.WithoutCancel(ctx) // the writes must outlive a cancelled eval
	logf := log.WithFields(log.Fields{"db": n.Name, "model_id": tm.ID, "eval_id": evalID})
	logf.WithField("examples", len(examples)).Info("training: eval started")
	finish := func(status, errText string) {
		if err := relational.FinishTrainedModelEval(dbCtx, n.DB, evalID, status, errText); err != nil {
			logf.WithError(err).Error("training: ending the eval failed")
		}
		logf.WithField("status", status).Info("training: eval ended")
	}
	defer func() {
		if r := recover(); r != nil {
			logf.Errorf("training: eval panicked: %v", r)
			finish(relational.TrainedModelEvalFailed, fmt.Sprintf("internal error: %v", r))
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
	judge := memory_core.WithRecording(m.llm, n.DB, CallEvalJudge)
	sink := func(_ context.Context, items []trainingeval.Item, p trainingeval.Progress) error {
		rows := make([]relational.TrainedModelEvalItem, len(items))
		for i, it := range items {
			rows[i] = relational.TrainedModelEvalItem{Idx: it.Idx, Batch: it.Batch, Instruction: it.Instruction, Input: it.Input,
				Reference: it.Reference, Response: it.Response, Status: it.Status, Error: it.Error, Score: it.Score, Reason: it.Reason,
				InputTokens: it.InputTokens, OutputTokens: it.OutputTokens, DurationMS: it.DurationMS}
		}
		e := relational.TrainedModelEval{ID: evalID, JudgeModel: p.JudgeModel, NumAnswered: p.Answered, NumJudged: p.Judged, NumFailed: p.Failed,
			NumCorrect: p.Correct, NumPartial: p.Partial, NumIncorrect: p.Incorrect, ScoreSum: p.ScoreSum, NumBatches: p.Batches,
			JudgeCalls: p.JudgeCalls, JudgeInputTokens: p.JudgeInputTokens, JudgeOutputTokens: p.JudgeOutputTokens, JudgeCostUSD: p.JudgeCostUSD}
		return relational.SaveTrainedModelEvalBatch(dbCtx, n.DB, e, rows)
	}
	_, err := trainingeval.Run(memory_core.WithStage(ctx, StageEvalJudge), cfg, examples, gen, judge, sink)
	switch {
	case err == nil:
		finish(relational.TrainedModelEvalCompleted, "")
	case errors.Is(err, context.Canceled) && m.ctx.Err() != nil:
		finish(relational.TrainedModelEvalFailed, "interrupted: the server shut down while the eval ran")
	case errors.Is(err, context.Canceled):
		finish(relational.TrainedModelEvalCancelled, "")
	default:
		logf.WithError(err).Warn("training: eval failed")
		finish(relational.TrainedModelEvalFailed, err.Error())
	}
}

// ListOf serves GET /v1/gb/training/models/{id}/evals[?db=<name>][&limit=<n>]: {"evals": [...]}, the evals of trained model
// {id} (relational.TrainedModelEval, without their items), newest first, at most limit (default 50, at most 500). 404 for an
// unknown model.
func (m *EvalManager) ListOf() http.HandlerFunc {
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
		limit := evalsDefaultLimit
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
		list, err := relational.ListTrainedModelEvals(r.Context(), n.DB, id, limit)
		if err != nil {
			log.WithError(err).Error("training: list evals failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the evals")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"evals": list})
	}
}

// GetOf serves GET /v1/gb/training/evals/{id}[?db=<name>]: {"eval": ..., "items": [...]}, one eval with the items stored so
// far (relational.TrainedModelEvalItem: question, the dataset's answer, the model's, score and reason), in dataset order.
// 404 for an unknown eval.
func (m *EvalManager) GetOf() http.HandlerFunc {
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
		ev, err := relational.GetTrainedModelEval(r.Context(), n.DB, id)
		if errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown eval")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read eval failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the eval")
			return
		}
		items, err := relational.ListTrainedModelEvalItems(r.Context(), n.DB, id)
		if err != nil {
			log.WithError(err).Error("training: list eval items failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the eval's items")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"eval": ev, "items": items})
	}
}

// CancelOf serves POST /v1/gb/training/evals/{id}/cancel[?db=<name>]: stops a running eval (it ends as "cancelled", keeping the
// batches done so far) and answers 202 with the eval. 404 for an unknown eval, 409 when it is not running.
func (m *EvalManager) CancelOf() http.HandlerFunc {
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
		ev, err := relational.GetTrainedModelEval(r.Context(), n.DB, id)
		if errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown eval")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read eval failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the eval")
			return
		}
		if ev.Status != relational.TrainedModelEvalRunning {
			httputil.Error(w, http.StatusConflict, "the eval is not running")
			return
		}
		m.mu.Lock()
		run := m.running[evalKey{n.Name, ev.TrainedModelID}]
		m.mu.Unlock()
		if run == nil || run.evalID != id {
			// Marked running, but nothing runs it (it cannot happen unless the bookkeeping failed): end it.
			if err := relational.FinishTrainedModelEval(r.Context(), n.DB, id, relational.TrainedModelEvalCancelled, ""); err != nil {
				log.WithError(err).Error("training: ending the eval failed")
			}
		} else {
			run.cancel()
		}
		httputil.WriteJSON(w, http.StatusAccepted, ev)
	}
}
