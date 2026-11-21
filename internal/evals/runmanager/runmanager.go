// Package runmanager runs the user evals inside the server: it starts the eval runs of a user (limits, validation, the sync of the def,
// the run recorded as pending), executes them in the background with the knowledge executor (a heartbeat tells the other servers that the
// run is alive and carries the cancel request), recovers the runs that a crash left behind, deletes the eval datasets that are past
// their retention, and reruns case runs. Every function acts as an owner, the user id.
//
// The evals use a remember manager of their own (not the one of the interactive routes), so an eval of a user never delays the remember
// calls of the others; it serializes the evals of all users.
package runmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/atif/atifstore"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/evals/knowledge"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
	"github.com/deepblue-phoenix/gligabao/internal/evals/usereval"
	"github.com/deepblue-phoenix/gligabao/internal/evals/userfiles"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_dataset"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	gberrors "github.com/deepblue-phoenix/gligabao/internal/sys/errors"
	"github.com/deepblue-phoenix/gligabao/internal/sys/server"

	log "github.com/sirupsen/logrus"
)

// The reasons recorded on the runs that did not end by themselves.
const (
	ReasonShutdown = "server shutdown"
	ReasonStale    = "server restarted or crashed while the run was in progress"
)

var (
	// ErrTooManyRuns, ErrDailyCap, ErrNoLLM and ErrBadOptions are those of package core (the API package maps them to HTTP statuses
	// and cannot import this one).
	ErrTooManyRuns = core.ErrTooManyRuns
	ErrDailyCap    = core.ErrDailyCap
	ErrNoLLM       = core.ErrNoLLM
	ErrBadOptions  = core.ErrBadOptions
	// ErrNotFound is the error of an unknown eval, run or case run of the owner (HTTP 404).
	ErrNotFound = evalsdb.ErrNotFound
)

// Limits are the limits and intervals of the manager (see the evals_* settings of the config). A zero field takes its default.
type Limits struct {
	// MaxRunsPerUser and MaxRuns are the runs pending or running at once, per user and in this server.
	MaxRunsPerUser, MaxRuns int
	// MaxRunCostUSD ends a run as errored ("cost cap reached") once its cases cost more; MaxUserCostUSDPerDay is the cost of the runs
	// of a user in the last 24 hours past which no run starts.
	MaxRunCostUSD, MaxUserCostUSDPerDay float64
	// DatasetRetention is how long after a run ended its eval datasets are kept.
	DatasetRetention time.Duration
	// HeartbeatInterval is the time between the beats of a running run; a run without a beat for HeartbeatStale is stale.
	HeartbeatInterval, HeartbeatStale time.Duration
	// SweepInterval is the time between the sweeps of stale runs, RetentionInterval between those of the eval datasets.
	SweepInterval, RetentionInterval time.Duration
}

// DefaultLimits are the defaults of section 6.4 and 6.5 of the design.
func DefaultLimits() Limits {
	return Limits{
		MaxRunsPerUser:       2,
		MaxRuns:              8,
		MaxRunCostUSD:        5,
		MaxUserCostUSDPerDay: 20,
		DatasetRetention:     14 * 24 * time.Hour,
		HeartbeatInterval:    15 * time.Second,
		HeartbeatStale:       2 * time.Minute,
		SweepInterval:        time.Minute,
		RetentionInterval:    time.Hour,
	}
}

// withDefaults fills the zero fields.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxRunsPerUser == 0 {
		l.MaxRunsPerUser = d.MaxRunsPerUser
	}
	if l.MaxRuns == 0 {
		l.MaxRuns = d.MaxRuns
	}
	if l.MaxRunCostUSD == 0 {
		l.MaxRunCostUSD = d.MaxRunCostUSD
	}
	if l.MaxUserCostUSDPerDay == 0 {
		l.MaxUserCostUSDPerDay = d.MaxUserCostUSDPerDay
	}
	if l.DatasetRetention == 0 {
		l.DatasetRetention = d.DatasetRetention
	}
	if l.HeartbeatInterval == 0 {
		l.HeartbeatInterval = d.HeartbeatInterval
	}
	if l.HeartbeatStale == 0 {
		l.HeartbeatStale = d.HeartbeatStale
	}
	if l.SweepInterval == 0 {
		l.SweepInterval = d.SweepInterval
	}
	if l.RetentionInterval == 0 {
		l.RetentionInterval = d.RetentionInterval
	}
	return l
}

// Config is what the manager works with.
type Config struct {
	// DB is the main database: the evals tables and the eval datasets are in it.
	DB *db.DB
	// Files holds the eval files of the users.
	Files userfiles.Store
	// FileLimits are the limits of an eval file; zero: DefaultFileLimits(Prices).
	FileLimits evalfile.Limits
	// LLM, Embedder, WebSearch and Geo are the server's (Geo nil: geo is off for every run). LLM nil: no run starts.
	LLM       memory_core.LLM
	Embedder  memory_core.Embedder
	WebSearch memory_core.WebSearcher
	Geo       memory_geo.Resolver
	// Prices is the price table, whose models are the ones a run may pick.
	Prices *llm.PriceTable
	// Trajectories is where the trajectories are stored (nil: the directory TrajectoryDir, else atifstore.Default).
	Trajectories  atifstore.Store
	TrajectoryDir string
	Limits        Limits
}

// RunOptions say how a run is started.
type RunOptions = core.RunOptions

// Manager starts and supervises the eval runs of the users. Make it with New; it stops with the context it was made with.
type Manager struct {
	cfg      Config
	limits   Limits
	store    evalsdb.Store
	rn       *runner.Runner
	runnerID string
	remember *memory_remember.Manager
	rerun    *knowledge.Rerunner
	ctx      context.Context
	env      usereval.Env

	// startMu makes the check of the limits and the recording of a run one step.
	startMu sync.Mutex
	mu      sync.Mutex
	running map[core.EvalRunID]context.CancelFunc
	wg      sync.WaitGroup
}

// DefaultFileLimits are the default limits of an eval file, with the models of the price table.
func DefaultFileLimits(prices *llm.PriceTable) evalfile.Limits {
	return evalfile.DefaultLimits(llmModels(prices),
		knowledge.ModelStages(),
		knowledge.ReasoningStages())
}

// llmModels are the names of the LLM models of the price table.
func llmModels(prices *llm.PriceTable) []string {
	var out []string
	for _, m := range llm.SupportedModels(prices) {
		if m.Kind == llm.KindLLM {
			out = append(out, m.Name)
		}
	}
	return out
}

// RunnerID is "<hostname>:<pid>:<start unix time>", what identifies this server on the runs it executes.
func RunnerID(start time.Time) string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return fmt.Sprintf("%s:%d:%d",
		host,
		os.Getpid(),
		start.Unix())
}

// New makes the manager: it starts the sweeps (stale runs, retention) and the remember manager of the evals, which stop with ctx. The
// stale runs of a crashed server are recovered right away (those that are not stale yet by the next sweep).
func New(ctx context.Context,
	cfg Config) (*Manager, error) {
	if cfg.DB == nil {
		return nil, errors.New("runmanager: no database")
	}
	if cfg.Files == nil {
		return nil, errors.New("runmanager: no store of eval files")
	}
	store, err := evals.StoreFor(cfg.DB.Backend)
	if err != nil {
		return nil, err
	}
	if cfg.FileLimits.Models == nil && cfg.FileLimits.MaxFileBytes == 0 {
		cfg.FileLimits = DefaultFileLimits(cfg.Prices)
	}
	m := &Manager{
		cfg:      cfg,
		limits:   cfg.Limits.withDefaults(),
		store:    store,
		rn:       runner.New(cfg.DB.Relational, store),
		runnerID: RunnerID(time.Now()),
		ctx:      ctx,
		running:  map[core.EvalRunID]context.CancelFunc{},
	}
	errSink, sinkErr := gberrors.SinkFor(cfg.DB.Backend, cfg.DB.Relational)
	if sinkErr != nil {
		errSink = nil
	}
	m.remember = memory_remember.NewManager(ctx,
		cfg.DB.Relational,
		cfg.LLM,
		memory_remember.WithErrSink(errSink))
	m.env = usereval.Env{
		Store:  cfg.Files,
		Limits: cfg.FileLimits,
		Sync:   m.sync,
	}
	m.rerun = knowledge.NewRerunnerWith(server.EvalEnv{
		Ctx:       ctx,
		DB:        cfg.DB,
		LLM:       cfg.LLM,
		Embedder:  cfg.Embedder,
		WebSearch: cfg.WebSearch,
		Geo:       cfg.Geo,
		Prices:    cfg.Prices,
	},
		knowledge.RerunnerOptions{
			Remember:      m.remember,
			Trajectories:  cfg.Trajectories,
			TrajectoryDir: cfg.TrajectoryDir,
			MaxRunCostUSD: m.limits.MaxRunCostUSD,
		})
	if err := m.Recover(ctx); err != nil {
		log.WithError(err).Warn("evals: recovering the stale runs failed")
	}
	m.wg.Add(1)
	go m.sweeps()
	return m, nil
}

// Env is the environment of the functions of package usereval (save, get, list and delete an eval, its files) with the sync of the
// defs into the database: the API calls them through it.
func (m *Manager) Env() usereval.Env { return m.env }

// Store is the functions of the evals database.
func (m *Manager) Store() evalsdb.Store { return m.store }

// Runner is the runner that records in the database of the manager.
func (m *Manager) Runner() *runner.Runner { return m.rn }

// Models are the LLM models of the price table: the ones a run or a rerun may pick.
func (m *Manager) Models() []llm.ModelInfo {
	var out []llm.ModelInfo
	for _, mi := range llm.SupportedModels(m.cfg.Prices) {
		if mi.Kind == llm.KindLLM {
			out = append(out, mi)
		}
	}
	return out
}

// CanRerun is whether the manager can rerun case runs (it has an LLM).
func (m *Manager) CanRerun() bool { return m.rerun != nil }

// CheckBudget returns ErrDailyCap if the owner has reached the cost cap of the day (what a commentary checks before it spends).
func (m *Manager) CheckBudget(ctx context.Context,
	owner string) error {
	if err := checkOwner(owner); err != nil {
		return err
	}
	spent, err := m.store.SumCostSince(ctx,
		m.cfg.DB.Relational,
		owner,
		time.Now().Add(-24*time.Hour))
	if err != nil {
		return err
	}
	if spent >= m.limits.MaxUserCostUSDPerDay {
		return ErrDailyCap
	}
	return nil
}

// Rerunner is the rerunner of the manager, nil if the server has no LLM. Use Rerun, which checks the limits.
func (m *Manager) Rerunner() *knowledge.Rerunner { return m.rerun }

// Wait blocks until the runs in progress and the sweeps have returned, which is after the context of the manager is done.
func (m *Manager) Wait() {
	m.wg.Wait()
	if m.rerun != nil {
		m.rerun.Wait()
	}
}

// sync puts the def of a user eval in the database.
func (m *Manager) sync(ctx context.Context,
	userID string,
	ev evalfile.UserEval) error {
	_, err := m.rn.SyncEval(ctx,
		userID,
		knowledge.Spec(ev))
	return err
}

// checkOwner refuses an empty owner and the owner of the training evals.
func checkOwner(owner string) error {
	if owner == "" || owner == core.SystemUser {
		return usereval.ErrInvalidUser
	}
	return nil
}

// checkLimits returns ErrTooManyRuns or ErrDailyCap if the owner may not start a run now.
func (m *Manager) checkLimits(ctx context.Context,
	owner string) error {
	active, err := m.store.CountActiveEvalRuns(ctx,
		m.cfg.DB.Relational,
		owner)
	if err != nil {
		return err
	}
	m.mu.Lock()
	local := len(m.running)
	m.mu.Unlock()
	if active >= m.limits.MaxRunsPerUser || local >= m.limits.MaxRuns {
		return ErrTooManyRuns
	}
	spent, err := m.store.SumCostSince(ctx,
		m.cfg.DB.Relational,
		owner,
		time.Now().Add(-24*time.Hour))
	if err != nil {
		return err
	}
	if spent >= m.limits.MaxUserCostUSDPerDay {
		return ErrDailyCap
	}
	return nil
}

// checkOptions validates the models and the reasoning of a run: supported models only, known stages and levels.
func (m *Manager) checkOptions(opts RunOptions) error {
	supported := llmModels(m.cfg.Prices)
	for stage, model := range opts.Models {
		if !contains(supported, model) {
			return fmt.Errorf("%w: models: %q for %s is not a supported model",
				ErrBadOptions,
				model,
				stage)
		}
	}
	if err := knowledge.ValidateModels(opts.Models); err != nil {
		return fmt.Errorf("%w: models: %v",
			ErrBadOptions,
			err)
	}
	if err := knowledge.ValidateReasoning(opts.Reasoning); err != nil {
		return fmt.Errorf("%w: reasoning: %v",
			ErrBadOptions,
			err)
	}
	return nil
}

func contains(list []string,
	s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// selectCases returns def with the cases called by names (all if empty), in the order of the eval.
func selectCases(def *core.EvalDef,
	names []string) (*core.EvalDef, error) {
	if len(names) == 0 {
		return def, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	out := *def
	out.Cases = nil
	for _, c := range def.Cases {
		if want[c.Name] {
			out.Cases = append(out.Cases, c)
			delete(want, c.Name)
		}
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("%w: the eval has no such case in: %v",
			ErrBadOptions,
			names)
	}
	return &out, nil
}

// Start starts a run of the eval of the owner called evalName and returns its ID while it goes on in the background. The order: check
// the limits, re-read and validate the eval file and sync the def (usereval.Prepare), record the run as pending, launch it. The errors:
// usereval.ErrInvalidUser, ErrNoLLM, ErrBadOptions, ErrTooManyRuns, ErrDailyCap, usereval.ErrNotFound (no such eval),
// *usereval.InvalidError, usereval.ErrStorage, usereval.ErrSync, or a failure of the database.
func (m *Manager) Start(ctx context.Context,
	owner,
	evalName string,
	opts RunOptions) (core.EvalRunID, error) {
	if err := checkOwner(owner); err != nil {
		return 0, err
	}
	if m.cfg.LLM == nil {
		return 0, ErrNoLLM
	}
	if err := m.checkOptions(opts); err != nil {
		return 0, err
	}
	if err := m.checkLimits(ctx, owner); err != nil {
		return 0, err
	}
	if _, err := usereval.Prepare(ctx,
		m.env,
		owner,
		evalName); err != nil {
		return 0, err
	}
	full, err := m.store.GetEvalDefByName(ctx,
		m.cfg.DB.Relational,
		owner,
		evalName)
	if err != nil {
		return 0, err
	}
	def, err := selectCases(full, opts.Cases)
	if err != nil {
		return 0, err
	}
	geo := opts.Geo == nil || *opts.Geo
	params := core.Params{
		"cases": opts.Cases,
		"geo":   geo && m.cfg.Geo != nil,
	}
	if len(opts.Models) > 0 {
		params["models"] = opts.Models
	}
	if len(opts.Reasoning) > 0 {
		params["reasoning"] = opts.Reasoning
	}

	// The check and the registration of the run are one step, so two starts cannot both pass the limit.
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if m.ctx.Err() != nil {
		return 0, memory_remember.ErrManagerClosed
	}
	if err := m.checkLimits(ctx, owner); err != nil {
		return 0, err
	}
	run, err := m.rn.CreateRun(ctx,
		def,
		m.runnerID,
		runner.EvalOptions{
			Params: params,
			Meta:   &core.KnowledgeEvalRunMeta{},
		})
	if err != nil {
		return 0, err
	}
	runCtx, cancel := context.WithCancel(m.ctx)
	m.mu.Lock()
	m.running[run.ID] = cancel
	m.mu.Unlock()
	m.wg.Add(1)
	go m.execute(runCtx,
		cancel,
		owner,
		def,
		run,
		geo && m.cfg.Geo != nil,
		opts)
	return run.ID, nil
}

// execute claims the run and runs it, with a heartbeat beside it, then records how it ended.
func (m *Manager) execute(ctx context.Context,
	cancel context.CancelFunc,
	owner string,
	def *core.EvalDef,
	run *core.EvalRun,
	geo bool,
	opts RunOptions) {
	defer m.wg.Done()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.running, run.ID)
		m.mu.Unlock()
	}()
	dbCtx := context.WithoutCancel(ctx)
	fail := func(err error) {
		log.WithError(err).WithField("eval_run_id", run.ID).Error("evals: the run could not be executed")
		m.finishWith(dbCtx,
			run,
			core.StatusErrored,
			err.Error())
	}
	if err := m.store.ClaimRun(dbCtx,
		m.cfg.DB.Relational,
		run.ID,
		m.runnerID,
		time.Now()); err != nil {
		if errors.Is(err, evalsdb.ErrNotFound) { // swept or deleted meanwhile: nothing to run
			return
		}
		fail(err)
		return
	}
	beats := make(chan struct{})
	go func() {
		defer close(beats)
		m.beat(ctx,
			cancel,
			run.ID)
	}()
	defer func() {
		cancel()
		<-beats
	}()

	var geoResolver memory_geo.Resolver
	if geo {
		geoResolver = m.cfg.Geo
	}
	x := &knowledge.Executor{
		Owner:         owner,
		DB:            m.cfg.DB,
		LLM:           m.cfg.LLM,
		Embedder:      m.cfg.Embedder,
		WebSearch:     m.cfg.WebSearch,
		Geo:           geoResolver,
		Models:        opts.Models,
		Reasoning:     opts.Reasoning,
		Remember:      m.remember,
		Trajectories:  m.cfg.Trajectories,
		TrajectoryDir: m.cfg.TrajectoryDir,
		MaxCostUSD:    m.limits.MaxRunCostUSD,
		ReadText: func(ctx context.Context,
			path string) (string, error) {
			return usereval.TextOf(ctx,
				m.env,
				owner,
				path)
		},
	}
	_, err := m.rn.RunEval(ctx,
		def,
		x,
		runner.EvalOptions{
			Run: run,
		})
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, knowledge.ErrCostCap) {
		log.WithError(err).WithField("eval_run_id", run.ID).Warn("evals: the run did not run to the end")
	}
	if m.ctx.Err() != nil && run.Status == core.StatusCancelled && run.Error == "" {
		// The server is shutting down: say so on the run (the runner left the reason empty).
		m.finishWith(dbCtx,
			run,
			core.StatusCancelled,
			ReasonShutdown)
	}
}

// finishWith ends the run with a status and a reason (it keeps the rest of the run as it is recorded).
func (m *Manager) finishWith(ctx context.Context,
	run *core.EvalRun,
	status core.Status,
	reason string) {
	end := time.Now().UTC().Truncate(time.Second)
	run.Status, run.Error = status, reason
	if run.FinishedAt == nil {
		run.FinishedAt = &end
	}
	if err := m.store.UpdateEvalRun(ctx,
		m.cfg.DB.Relational,
		run); err != nil {
		log.WithError(err).WithField("eval_run_id", run.ID).Error("evals: recording the end of the run failed")
	}
}

// beat records the heartbeat of the run every HeartbeatInterval, and cancels the run when a cancel was requested or the run has
// been ended by another party (a sweep).
func (m *Manager) beat(ctx context.Context,
	cancel context.CancelFunc,
	runID core.EvalRunID) {
	t := time.NewTicker(m.limits.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			requested, err := m.store.Heartbeat(context.WithoutCancel(ctx),
				m.cfg.DB.Relational,
				runID,
				time.Now())
			switch {
			case errors.Is(err, evalsdb.ErrNotFound): // final already: stop executing it
				cancel()
				return
			case err != nil:
				log.WithError(err).WithField("eval_run_id", runID).Warn("evals: heartbeat failed")
			case requested:
				cancel()
				return
			}
		}
	}
}

// Cancel asks the run of the owner to stop: the cancel request is recorded (the server that executes the run sees it at its next beat)
// and, when this server executes it, the run is cancelled at once. The errors: ErrNotFound (no such run of the owner),
// evalsdb.ErrRunFinished (it has ended).
func (m *Manager) Cancel(ctx context.Context,
	owner string,
	runID core.EvalRunID) error {
	if err := checkOwner(owner); err != nil {
		return err
	}
	if err := m.store.RequestCancel(ctx,
		m.cfg.DB.Relational,
		owner,
		runID); err != nil {
		return err
	}
	m.mu.Lock()
	cancel := m.running[runID]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Running is the number of runs this server is executing.
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.running)
}

// Recover ends as errored (ReasonStale) the knowledge runs of any server whose heartbeat is older than HeartbeatStale. It returns
// nil when there was nothing to do.
func (m *Manager) Recover(ctx context.Context) error {
	now := time.Now()
	_, err := m.store.FailStale(ctx,
		m.cfg.DB.Relational,
		core.TypeKnowledge,
		now.Add(-m.limits.HeartbeatStale),
		ReasonStale,
		now)
	return err
}

// Retain deletes the eval datasets (and their vectors) of the runs that ended more than DatasetRetention ago. It returns the number
// of runs whose datasets were deleted.
func (m *Manager) Retain(ctx context.Context) (int, error) {
	refs, err := m.store.ListRunsWithEvalDatasetsBefore(ctx,
		m.cfg.DB.Relational,
		time.Now().Add(-m.limits.DatasetRetention),
		100)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ref := range refs {
		if err := m.DeleteDatasets(ctx,
			ref.UserID,
			ref.RunID); err != nil {
			log.WithError(err).WithField("eval_run_id", ref.RunID).Warn("evals: retention: deleting the datasets of a run failed")
			continue
		}
		n++
	}
	return n, nil
}

// DeleteDatasets deletes the eval datasets of a run of the owner and their vectors (when a run or an eval is deleted, and by Retain).
func (m *Manager) DeleteDatasets(ctx context.Context,
	owner string,
	runID core.EvalRunID) error {
	_, err := memory_dataset.DeleteEvalDatasets(ctx,
		m.cfg.DB.Relational,
		m.cfg.DB.Vector,
		owner,
		int64(runID))
	return err
}

// sweeps runs Recover and Retain on their intervals until the context ends.
func (m *Manager) sweeps() {
	defer m.wg.Done()
	sweep := time.NewTicker(m.limits.SweepInterval)
	retain := time.NewTicker(m.limits.RetentionInterval)
	defer sweep.Stop()
	defer retain.Stop()
	if _, err := m.Retain(m.ctx); err != nil && m.ctx.Err() == nil {
		log.WithError(err).Warn("evals: retention sweep failed")
	}
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-sweep.C:
			if err := m.Recover(m.ctx); err != nil && m.ctx.Err() == nil {
				log.WithError(err).Warn("evals: sweep of the stale runs failed")
			}
		case <-retain.C:
			if _, err := m.Retain(m.ctx); err != nil && m.ctx.Err() == nil {
				log.WithError(err).Warn("evals: retention sweep failed")
			}
		}
	}
}

// Rerun reruns a case run of the owner with other models or reasoning levels (see knowledge.Rerunner): the limits of runs in progress
// and of the daily cost apply, and one rerun runs at a time per user. The errors are those of Rerunner.Rerun, ErrTooManyRuns, ErrDailyCap
// and ErrNoLLM.
func (m *Manager) Rerun(ctx context.Context,
	owner string,
	src core.CaseRunID,
	opts core.RerunOptions) (core.CaseRunID, error) {
	if err := checkOwner(owner); err != nil {
		return 0, err
	}
	if m.rerun == nil {
		return 0, ErrNoLLM
	}
	if err := m.checkLimits(ctx, owner); err != nil {
		return 0, err
	}
	return m.rerun.Rerun(ctx,
		owner,
		src,
		opts)
}
