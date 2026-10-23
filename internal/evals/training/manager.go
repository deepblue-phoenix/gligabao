package training

// The Manager starts the evals of trained models, runs them in the background and lists, reads, cancels and watches them. An eval
// is an eval run (core.EvalRun of the Type training) of an eval of bin/evals_training/*.json against a trained model: the model is
// asked the cases, which it never saw in training, and the LLM judges the answers, so the score says how well it generalizes. The
// eval is named after the training dataset its cases ask about, so a model trained on a dataset has the eval of that name unless
// another is asked for. The work is that of Executor and runner.Runner; this file wires it to the sidecar (the model), the LLM (the
// judge) and the database, and keeps what the HTTP side needs: the live feed and the views (view.go).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
	trainingcore "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

// Inferer runs a prompt through a trained model on the sidecar; *sidecar.Client is one.
type Inferer interface {
	Infer(ctx context.Context, req sidecar.InferRequest) (sidecar.InferResponse, error)
}

// Manager starts the evals of trained models and runs them in the background, one at a time per trained model. Evals that were still
// running when the server last stopped are marked errored when the manager is made.
type Manager struct {
	// DB holds the trained models and the evals. Sc answers the questions and LLM judges (nil: starting an eval is not possible).
	DB  *sql.DB
	Sc  Inferer
	LLM memory_core.LLM
	// Store is how the evals are stored and read (the backend of DB).
	Store db.Store

	dirs []string // the evals directories
	ctx  context.Context

	mu      sync.Mutex
	running map[int64]*runningEval   // by trained model ID
	feeds   map[core.EvalRunID]*Feed // the live feeds of the evals that run
	wg      sync.WaitGroup
}

type runningEval struct {
	runID  core.EvalRunID // 0 until the run is recorded
	cancel context.CancelFunc
}

// NewManager makes the manager of the evals of the trained models in d (which has the schema of evals.Open), whose evals are in
// the directories evalDirs. sc answers the questions and llm judges (the callers check for nil). The evals stop when ctx is done. Evals
// that a restart interrupted (still pending or running in the database) are marked errored.
func NewManager(ctx context.Context, d *sql.DB, store db.Store, evalDirs []string, sc Inferer, llm memory_core.LLM) *Manager {
	m := &Manager{DB: d, Sc: sc, LLM: llm, Store: store, dirs: evalDirs, ctx: ctx,
		running: map[int64]*runningEval{}, feeds: map[core.EvalRunID]*Feed{}}
	if k, err := store.FailRunning(ctx, d, core.TypeTraining, "interrupted: the server restarted while the eval ran", time.Now()); err != nil {
		log.WithError(err).Warn("training evals: could not mark interrupted evals as errored")
	} else if k > 0 {
		log.Infof("training evals: %d eval(s) were interrupted by a restart and marked errored", k)
	}
	return m
}

// Wait blocks until the running evals have ended (after the manager's context is done, or for tests).
func (m *Manager) Wait() { m.wg.Wait() }

// Done is closed when the manager's context is done.
func (m *Manager) Done() <-chan struct{} { return m.ctx.Done() }

// EvalDirs are the directories the evals are read from, in order.
func (m *Manager) EvalDirs() []string { return m.dirs }

// LoadEvals reads the evals of the manager's directories (see LoadEvals). A directory that does not exist has no evals (a program may
// list directories that exist only in some deployments).
func (m *Manager) LoadEvals() ([]Eval, error) {
	var dirs []string
	for _, d := range m.dirs {
		if _, err := os.Stat(d); err == nil || !errors.Is(err, fs.ErrNotExist) {
			dirs = append(dirs, d)
		}
	}
	return LoadEvals(dirs...)
}

// HasEval reports whether the evals directories have an eval called name.
func (m *Manager) HasEval(name string) bool {
	evals, err := m.LoadEvals()
	return err == nil && slices.ContainsFunc(evals, func(e Eval) bool { return e.Name == name })
}

// getModel reads trained model id, as an *Error.
func (m *Manager) getModel(ctx context.Context, id int64) (*trainingdb.TrainedModel, error) {
	tm, err := trainingcore.GetModel(ctx, m.DB, id)
	var te *trainingcore.Error
	switch {
	case err == nil:
		return tm, nil
	case errors.As(err, &te) && te.Kind == trainingcore.KindNotFound:
		return nil, notFoundErr(te.Msg)
	case errors.As(err, &te):
		return nil, internalErr(te.Msg, te.Err)
	}
	return nil, internalErr("failed to read the trained model", err)
}

// Generator makes the engine's GenerateFunc of trained model tm: one sidecar inference per question, with the settings of params.
func Generator(sc Inferer, tm *trainingdb.TrainedModel, params RunParams) GenerateFunc {
	return func(ctx context.Context, prompt string) (Generation, error) {
		ctx, cancel := context.WithTimeout(ctx, trainingcore.InferTimeout)
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

// Start records an eval of trained model modelID and starts it in the background, returning the recorded run (status running).
// Errors are *Error: KindInvalid for bad options, an unknown eval or case, or no eval to default to (the model has no dataset, or
// no eval has its name), KindNotFound for an unknown model, KindConflict if the model has an eval running already, KindUnavailable
// without a sidecar or an LLM, KindInternal.
func (m *Manager) Start(ctx context.Context, modelID int64, opts Options) (*RunView, error) {
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
	evals, err := m.LoadEvals()
	if err != nil {
		log.WithError(err).Error("training evals: load the evals failed")
		return nil, internalErr("failed to load the evals", err)
	}
	name := strings.TrimSpace(opts.Eval)
	if name == "" {
		if name = strings.TrimSuffix(tm.Dataset, ".jsonl"); name == "" || !slices.ContainsFunc(evals, func(e Eval) bool { return e.Name == name }) {
			return nil, invalidErr("there is no eval named like this model's training dataset; set \"eval\" to one of: " + strings.Join(evalfileNames(evals), ", "))
		}
	}
	selected, err := SelectEvals(evals, name, opts.Cases)
	if err != nil {
		return nil, invalidErr(err.Error())
	}
	if len(selected) != 1 {
		return nil, invalidErr(fmt.Sprintf("eval %q has no cases", name))
	}
	var chosen []string // the cases to ask, by name, in the order of the eval
	for _, c := range selected[0].Cases {
		chosen = append(chosen, c.Name)
	}
	if params.Limit > 0 && params.Limit < len(chosen) {
		chosen = chosen[:params.Limit]
	}
	if len(opts.Cases) > 0 {
		params.Cases = chosen
	}

	// One eval at a time per model: it would otherwise compete with itself for the model's GPU container.
	runCtx, cancel := context.WithCancel(m.ctx)
	cur := &runningEval{cancel: cancel}
	m.mu.Lock()
	if _, busy := m.running[tm.ID]; busy {
		m.mu.Unlock()
		cancel()
		return nil, conflictErr("an eval of this model is running already")
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
	r := runner.New(m.DB, m.Store)
	// The whole eval is put in the database (so that its order stays that of the file); the run asks the chosen cases of it.
	def, err := r.SyncEval(dbCtx, Spec(selectedFull(evals, name)))
	if err != nil {
		release()
		log.WithError(err).WithField("model_id", tm.ID).Error("training evals: putting the eval in the database failed")
		return nil, internalErr("failed to record the eval", err)
	}
	def.Cases = slices.DeleteFunc(def.Cases, func(c core.CaseDef) bool { return !slices.Contains(chosen, c.Name) })

	var xr *runner.ExperimentRecorder
	var runOpts runner.EvalOptions
	if x := strings.TrimSpace(opts.Experiment); x != "" {
		var cfgParams core.Params
		if len(opts.ExperimentConfig) > 0 {
			cfgParams = core.Params(opts.ExperimentConfig)
		}
		xd, err := r.EnsureExperimentWith(dbCtx, x, opts.ExperimentDescription, cfgParams, []core.EvalDefID{def.ID})
		if err == nil {
			xr, err = r.StartExperimentRun(dbCtx, xd, params.Params())
		}
		if err != nil {
			release()
			log.WithError(err).WithField("experiment", x).Error("training evals: adding the eval to its experiment failed")
			return nil, internalErr("failed to add the eval to the experiment", err)
		}
		id := xr.Run().ID
		runOpts.ExperimentRunID = &id
	}

	feed := NewFeed()
	created := make(chan RunView, 1)
	var run *core.EvalRun // the eval run the recorder keeps, set by onCreated
	runOpts.Params = params.Params()
	runOpts.Meta = &core.TrainingEvalRunMeta{TrainedModelID: tm.ID}
	runOpts.OnCreated = func(er *core.EvalRun) {
		run = er
		m.mu.Lock()
		cur.runID = er.ID
		m.feeds[er.ID] = feed
		m.mu.Unlock()
		created <- ViewOfRun(er, def.Name)
	}
	failed := make(chan error, 1)

	x := &Executor{
		Config:   cfg,
		Generate: Generator(m.Sc, tm, params),
		Judge:    memory_core.WithRecording(m.LLM, m.DB, CallJudge),
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer release()
		m.run(runCtx, r, def, x, runOpts, xr, feed, &run, failed)
	}()
	select {
	case v := <-created:
		return &v, nil
	case err := <-failed:
		log.WithError(err).WithField("model_id", tm.ID).Error("training evals: recording the eval failed")
		return nil, internalErr("failed to record the eval", err)
	}
}

// selectedFull is the eval called name of evals, with all its cases.
func selectedFull(evals []Eval, name string) Eval {
	i := slices.IndexFunc(evals, func(e Eval) bool { return e.Name == name })
	return evals[i]
}

func evalfileNames(evals []Eval) []string {
	names := make([]string, len(evals))
	for i, e := range evals {
		names[i] = e.Name
	}
	return names
}

// run is the eval's goroutine: it runs the eval with the runner, which stores every finished batch, and ends the records. failed
// gets the error if the eval run could not even be recorded.
func (m *Manager) run(ctx context.Context, r *runner.Runner, def *core.EvalDef, x *Executor, opts runner.EvalOptions, xr *runner.ExperimentRecorder,
	feed *Feed, run **core.EvalRun, failed chan<- error) {
	dbCtx := context.WithoutCancel(ctx) // the writes must outlive a cancelled eval
	logf := log.WithField("eval", def.Name)
	logf.WithField("cases", len(def.Cases)).Info("training evals: eval started")
	names := make([]string, len(def.Cases))
	for i, c := range def.Cases {
		names[i] = c.Name
	}
	x.OnAnswer = func(res CaseResult) {
		feed.Answer(ViewOfResult((*run).ID, names[res.Idx], res))
	}
	x.OnBatch = func(cases []CaseResult, _ Progress) {
		views := make([]CaseView, len(cases))
		for i, c := range cases {
			views[i] = ViewOfResult((*run).ID, names[c.Idx], c)
		}
		feed.Batch(ViewOfRun(*run, def.Name), views)
	}

	ended, err := m.runRecovering(ctx, r, def, x, opts, run, logf)
	if ended != nil && ended.Status == core.StatusCancelled && m.ctx.Err() != nil {
		// Stopped by the server shutting down, not by the user.
		ended.Status, ended.Error = core.StatusErrored, "interrupted: the server shut down while the eval ran"
		if uerr := m.Store.UpdateEvalRun(dbCtx, m.DB, ended); uerr != nil {
			logf.WithError(uerr).Error("training evals: ending the eval failed")
		}
	}
	if xr != nil {
		if ended != nil {
			if aerr := xr.Add(dbCtx, ended); aerr != nil {
				logf.WithError(aerr).Error("training evals: updating the experiment run failed")
			}
		}
		if ferr := xr.Finish(dbCtx, err); ferr != nil {
			logf.WithError(ferr).Error("training evals: ending the experiment run failed")
		}
	}
	if ended == nil { // it was not recorded
		failed <- err
		return
	}
	logf.WithField("status", ended.Status).Info("training evals: eval ended")
	if err != nil && ended.Status == core.StatusErrored {
		logf.WithError(err).Warn("training evals: eval failed")
	}
	m.publishEnd(dbCtx, feed, ended.ID, def.Name)
}

// runRecovering runs the eval with the runner; a panic ends the eval run (if it was recorded) as errored.
func (m *Manager) runRecovering(ctx context.Context, r *runner.Runner, def *core.EvalDef, x *Executor, opts runner.EvalOptions,
	run **core.EvalRun, logf *log.Entry) (ended *core.EvalRun, err error) {
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		logf.Errorf("training evals: eval panicked: %v", p)
		err = fmt.Errorf("internal error: %v", p)
		if cur := *run; cur != nil {
			end := time.Now().UTC().Truncate(time.Second)
			cur.Status, cur.Error, cur.FinishedAt = core.StatusErrored, err.Error(), &end
			if uerr := m.Store.UpdateEvalRun(context.WithoutCancel(ctx), m.DB, cur); uerr != nil {
				logf.WithError(uerr).Error("training evals: ending the eval failed")
			}
			ended = cur
		}
	}()
	return r.RunEval(memory_core.WithStage(ctx, StageJudge), def, x, opts)
}

// publishEnd announces the end of eval id on its feed, with the run as stored.
func (m *Manager) publishEnd(ctx context.Context, f *Feed, id core.EvalRunID, eval string) {
	m.mu.Lock()
	if m.feeds[id] == f {
		delete(m.feeds, id)
	}
	m.mu.Unlock()
	if run, err := m.Store.GetEvalRun(ctx, m.DB, id); err == nil {
		f.End(ViewOfRun(run, eval))
	} else {
		log.WithError(err).WithField("run_id", id).Warn("training evals: read the ended eval for its feed failed")
		f.End(RunView{ID: int64(id), Kind: KindEval})
	}
}
