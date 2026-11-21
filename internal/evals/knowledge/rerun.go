package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/deepblue-phoenix/gligabao/internal/atif/atifstore"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_geo"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_remember"
	"github.com/deepblue-phoenix/gligabao/internal/sys/server"
	log "github.com/sirupsen/logrus"
)

// Rerunner reruns a case of the evals database (of the owner) for the run manager (package runmanager, whose Rerun checks the limits first).
// The case is run again,
// from the input it was given the first time, with another model for every LLM stage of the remember pipeline and/or of the query
// (see PipelineModels) and/or another reasoning level for them (see PipelineReasoning); everything else (the other models and
// reasoning levels the run had, the checks and settings of the case)
// stays. The rerun is recorded like any run: as an EvalRun of its own with that one case (of the same eval), in the experiment run that owns the case
// run it repeats if it had one (whose counts and cost then include it). Its Params say what it is
// (core.ParamRerunOf and its siblings). One rerun runs at a time per user.
type Rerunner struct {
	env  server.EvalEnv
	opts RerunnerOptions
	mu   sync.Mutex
	busy map[string]bool // the owners that have a rerun going: one rerun at a time per user
	wg   sync.WaitGroup
}

// RerunnerOptions are what a rerunner shares with the run manager that makes it.
type RerunnerOptions struct {
	// Remember is the remember manager of the evals (see Executor.Remember); nil: every case makes its own.
	Remember *memory_remember.Manager
	// Trajectories is where the trajectories are stored (see Executor.Trajectories).
	Trajectories atifstore.Store
	// TrajectoryDir is the directory of the file store (see Executor.TrajectoryDir).
	TrajectoryDir string
	// MaxRunCostUSD is the cost cap of the rerun (0: none).
	MaxRunCostUSD float64
}

// NewRerunnerWith makes the rerunner of the environment with options: it records in the shared database env.DB (never closed by the
// rerunner). nil if there is no LLM or no database.
func NewRerunnerWith(env server.EvalEnv,
	opts RerunnerOptions) *Rerunner {
	if env.LLM == nil || env.DB == nil {
		return nil
	}
	return &Rerunner{
		env:  env,
		opts: opts,
		busy: map[string]bool{},
	}
}

// Models are the LLM models of the price table.
func (r *Rerunner) Models() []llm.ModelInfo {
	var out []llm.ModelInfo
	for _, m := range llm.SupportedModels(r.env.Prices) {
		if m.Kind == llm.KindLLM {
			out = append(out, m)
		}
	}
	return out
}

// Wait waits for the reruns in progress to end (they stop with the context of the environment, and record how they ended).
func (r *Rerunner) Wait() { r.wg.Wait() }

func (r *Rerunner) acquire(owner string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy[owner] {
		return false
	}
	r.busy[owner] = true
	return true
}

func (r *Rerunner) release(owner string) {
	r.mu.Lock()
	delete(r.busy, owner)
	r.mu.Unlock()
}

// Rerun starts the rerun of case run src, which goes on in the background, and returns the ID of the
// new case run as soon as it is recorded (running); follow it through the eval database. The errors are ErrNotFound (no such case
// run), core.ErrRerunRefused (it cannot be rerun, the error says why), core.ErrRerunBusy, memory_core.ErrInvalidModels,
// memory_core.ErrInvalidReasoning, or a failure of the database.
func (r *Rerunner) Rerun(ctx context.Context,
	owner string,
	src core.CaseRunID,
	opts core.RerunOptions) (core.CaseRunID, error) {
	if owner == "" || owner == core.SystemUser {
		return 0, evalsdb.ErrInvalidOwner
	}
	rememberModels, queryModels, err := PipelineModels(opts.RememberModel, opts.QueryModel)
	if err != nil {
		return 0, err
	}
	rememberReasoning, queryReasoning, err := PipelineReasoning(opts.RememberReasoning, opts.QueryReasoning)
	if err != nil {
		return 0, err
	}
	if !r.acquire(owner) {
		return 0, core.ErrRerunBusy
	}
	launched := false
	defer func() {
		if !launched {
			r.release(owner)
		}
	}()

	database, closeDB, err := r.database()
	if err != nil {
		return 0, err
	}
	defer func() {
		if !launched {
			closeDB()
		}
	}()
	store, err := evals.StoreFor(database.Backend)
	if err != nil {
		return 0, err
	}
	rn := runner.New(database.Relational, store)
	p, err := planRerun(ctx,
		rn,
		owner,
		src)
	if err != nil {
		return 0, err
	}

	params := core.Params{}
	for k, v := range p.evalRun.Params {
		params[k] = v
	}
	params["cases"] = []string{p.def.Cases[0].Name}
	params[core.ParamRerunOf] = int64(src)
	setOrDelete(params,
		core.ParamRememberModel,
		opts.RememberModel)
	setOrDelete(params,
		core.ParamQueryModel,
		opts.QueryModel)
	setOrDelete(params,
		core.ParamRememberReasoning,
		strings.ToLower(strings.TrimSpace(opts.RememberReasoning)))
	setOrDelete(params,
		core.ParamQueryReasoning,
		strings.ToLower(strings.TrimSpace(opts.QueryReasoning)))

	var geo memory_geo.Resolver
	if on, ok := params["geo"].(bool); !ok || on {
		if geo = r.env.Geo; geo == nil {
			geo = memory_geo.Default()
		}
	}
	started := make(chan core.CaseRunID, 1)
	ex := &Executor{
		Owner:             owner,
		DB:                database,
		LLM:               r.env.LLM,
		Embedder:          r.env.Embedder,
		WebSearch:         r.env.WebSearch,
		Geo:               geo,
		Models:            stringMap(params["models"]),
		Reasoning:         stringMap(params["reasoning"]),
		RememberModels:    rememberModels,
		QueryModels:       queryModels,
		RememberReasoning: rememberReasoning,
		QueryReasoning:    queryReasoning,
		Remember:          r.opts.Remember,
		Trajectories:      r.opts.Trajectories,
		TrajectoryDir:     r.opts.TrajectoryDir,
		MaxCostUSD:        r.opts.MaxRunCostUSD,
		OnCaseStart: func(cr *core.CaseRun) {
			select {
			case started <- cr.ID:
			default:
			}
		},
	}
	done := make(chan error, 1)
	launched = true
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.release(owner)
		defer closeDB()
		opts := runner.EvalOptions{
			ExperimentRunID: p.evalRun.ExperimentRunID,
			Params:          params,
			Meta:            &core.KnowledgeEvalRunMeta{},
		}
		run, err := rn.RunEval(r.env.Ctx,
			p.def,
			ex,
			opts)
		if run != nil && p.experiment != nil {
			// Recorded even if the server is shutting down: the run is in the experiment, so its counts must be too.
			if addErr := p.experiment.Add(context.WithoutCancel(r.env.Ctx), run); addErr != nil {
				log.WithError(addErr).WithField("eval_run_id", run.ID).Error("eval rerun: add the rerun to its experiment run failed")
			}
		}
		if err != nil {
			log.WithError(err).WithField("rerun_of", src).Warn("eval rerun did not run to the end")
		}
		done <- err
	}()

	select {
	case id := <-started:
		return id, nil
	case err := <-done: // ended without starting the case
		select {
		case id := <-started:
			return id, nil
		default:
		}
		if err == nil {
			err = errors.New("the rerun ended without running the case")
		}
		return 0, err
	case <-ctx.Done():
		return 0, ctx.Err() // the rerun goes on in the background
	}
}

// database is the database a rerun records in and its close function: the shared one (closing does nothing).
func (r *Rerunner) database() (*db.DB, func(), error) {
	return r.env.DB, func() {}, nil
}

// rerunPlan is what a rerun runs and where it is recorded.
type rerunPlan struct {
	// def is the eval def with the one case to run, whose input is the one the case run was given.
	def *core.EvalDef
	// evalRun is the eval run of the case run that is rerun (its Params are the base of the rerun's).
	evalRun *core.EvalRun
	// experiment is the recorder of the experiment run the rerun is added to (nil if the eval run had none).
	experiment *runner.ExperimentRecorder
}

// planRerun reads the case run src and checks that it can be rerun: it is a knowledge case run that has ended, its experiment run (if
// any) has ended, and its case still exists.
func planRerun(ctx context.Context,
	rn *runner.Runner,
	owner string,
	src core.CaseRunID) (*rerunPlan, error) {
	refuse := func(format string,
		args ...any) error {
		return fmt.Errorf("%w: %s",
			core.ErrRerunRefused,
			fmt.Sprintf(format, args...))
	}
	q, s := rn.DB, rn.Store
	cr, err := s.GetCaseRun(ctx,
		q,
		owner,
		src)
	if err != nil {
		return nil, err
	}
	if cr.Type != core.TypeKnowledge {
		return nil, evalsdb.ErrNotFound
	}
	if !cr.Status.Finished() {
		return nil, refuse("the case run has not ended (it is %s)", cr.Status)
	}
	er, err := s.GetEvalRun(ctx,
		q,
		owner,
		cr.EvalRunID)
	if err != nil {
		return nil, err
	}
	p := &rerunPlan{
		evalRun: er,
	}
	if er.ExperimentRunID != nil {
		if p.experiment, err = rn.ResumeExperimentRun(ctx,
			owner,
			*er.ExperimentRunID); err != nil {
			return nil, err
		}
		if st := p.experiment.Run().Status; !st.Finished() {
			return nil, refuse("its experiment run is still %s: wait for it to end", st)
		}
	}
	ed, err := s.GetEvalDef(ctx,
		q,
		owner,
		er.DefID)
	if err != nil {
		return nil, err
	}
	full, err := s.GetEvalDefByName(ctx,
		q,
		owner,
		ed.Name)
	if errors.Is(err, evalsdb.ErrNotFound) || err == nil && full.ID != ed.ID {
		return nil, refuse("the eval %q no longer exists", ed.Name)
	}
	if err != nil {
		return nil, err
	}
	var c *core.CaseDef
	for i := range full.Cases {
		if full.Cases[i].ID == cr.DefID {
			c = &full.Cases[i]
		}
	}
	dm, ok := (*core.KnowledgeCaseDefMeta)(nil), false
	if c != nil {
		dm, ok = c.Meta.(*core.KnowledgeCaseDefMeta)
	}
	if c == nil || !ok || dm == nil {
		return nil, refuse("the case no longer exists in the eval %q", ed.Name)
	}

	// The case is given what it was given the first time (the texts of its files as they were read then), not what the files say now;
	// the checks and settings are those of the case as it is defined now.
	meta := *dm
	if km, ok := cr.Meta.(*core.KnowledgeCaseRunMeta); ok && km != nil && (len(km.Input.Remember) > 0 || km.Input.Query != "") {
		meta.KnowledgeInput = km.Input
	}
	meta.Remember = append([]core.KnowledgeArtifact(nil), meta.Remember...)
	for i, a := range meta.Remember {
		if a.Text != "" {
			meta.Remember[i].TextFile = "" // the text is there: the file is only where it came from
		} else if a.TextFile != "" {
			return nil, refuse("remember #%d is the file %s, which was not read when the case ran",
				i+1,
				a.TextFile)
		}
	}
	rc := *c
	rc.Meta = &meta
	def := *full
	def.Cases = []core.CaseDef{rc}
	p.def = &def
	return p, nil
}

// setOrDelete sets params[key] to v, or removes it if v is empty.
func setOrDelete(params core.Params,
	key,
	v string) {
	if v == "" {
		delete(params, key)
	} else {
		params[key] = v
	}
}

// stringMap reads a models or reasoning map as it comes back from JSON (a map of any); nil if it is not one.
func stringMap(v any) map[string]string {
	var out map[string]string
	switch m := v.(type) {
	case map[string]string:
		out = make(map[string]string, len(m))
		for k, s := range m {
			out[k] = s
		}
	case map[string]any:
		out = make(map[string]string, len(m))
		for k, x := range m {
			if s, ok := x.(string); ok {
				out[k] = s
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
