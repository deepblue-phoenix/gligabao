package training

// Listing, reading, cancelling and watching the evals of the Manager, and the training experiments.

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/db"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

// evalNames reads the names of eval defs by their ID, each once.
type evalNames struct {
	m     *Manager
	names map[core.EvalDefID]string
}

func (m *Manager) evalNames() *evalNames { return &evalNames{m, map[core.EvalDefID]string{}} }

func (e *evalNames) of(ctx context.Context, id core.EvalDefID) (string, error) {
	if n, ok := e.names[id]; ok {
		return n, nil
	}
	d, err := e.m.Store.GetEvalDef(ctx, e.m.DB, id)
	if err != nil {
		return "", err
	}
	e.names[id] = d.Name
	return d.Name, nil
}

// views turns eval runs into views, with the names of their evals.
func (m *Manager) views(ctx context.Context, runs []core.EvalRun) ([]RunView, error) {
	names := m.evalNames()
	out := make([]RunView, len(runs))
	for i := range runs {
		name, err := names.of(ctx, runs[i].DefID)
		if err != nil {
			log.WithError(err).Error("training evals: read the eval of a run failed")
			return nil, internalErr("failed to read the evals", err)
		}
		out[i] = ViewOfRun(&runs[i], name)
	}
	return out, nil
}

// ListRuns returns the evals of trained model modelID (without their cases), newest first, at most limit. Errors are *Error:
// KindNotFound for an unknown model, KindInternal.
func (m *Manager) ListRuns(ctx context.Context, modelID int64, limit int) ([]RunView, error) {
	if _, err := m.getModel(ctx, modelID); err != nil {
		return nil, err
	}
	list, err := m.Store.ListTrainingEvalRuns(ctx, m.DB, modelID, limit)
	if err != nil {
		log.WithError(err).Error("training evals: list evals failed")
		return nil, internalErr("failed to list the evals", err)
	}
	return m.views(ctx, list)
}

// run0 reads eval run id (without its case runs) if it is a training one.
func (m *Manager) run0(ctx context.Context, id core.EvalRunID) (*core.EvalRun, error) {
	run, err := m.Store.GetEvalRun(ctx, m.DB, id)
	if errors.Is(err, db.ErrNotFound) || (err == nil && run.Type != core.TypeTraining) {
		return nil, notFoundErr("unknown eval")
	} else if err != nil {
		log.WithError(err).Error("training evals: read eval failed")
		return nil, internalErr("failed to read the eval", err)
	}
	return run, nil
}

// GetRun reads eval id, without its cases. Errors are *Error: KindNotFound, KindInternal.
func (m *Manager) GetRun(ctx context.Context, id int64) (*RunView, error) {
	run, err := m.run0(ctx, core.EvalRunID(id))
	if err != nil {
		return nil, err
	}
	vs, err := m.views(ctx, []core.EvalRun{*run})
	if err != nil {
		return nil, err
	}
	return &vs[0], nil
}

// RunCases reads the cases stored so far of eval id, in the order of the eval. Errors are *Error: KindInternal.
func (m *Manager) RunCases(ctx context.Context, id int64) ([]CaseView, error) {
	return m.caseViews(ctx, core.EvalRunID(id))
}

func (m *Manager) caseViews(ctx context.Context, id core.EvalRunID) ([]CaseView, error) {
	run, err := m.Store.GetEvalRun(ctx, m.DB, id)
	if err != nil {
		log.WithError(err).Error("training evals: read eval failed")
		return nil, internalErr("failed to read the eval's cases", err)
	}
	defs, err := m.Store.ListCaseDefs(ctx, m.DB, run.DefID)
	if err != nil {
		log.WithError(err).Error("training evals: list the case defs failed")
		return nil, internalErr("failed to read the eval's cases", err)
	}
	names := make(map[core.CaseDefID]string, len(defs))
	for _, d := range defs {
		names[d.ID] = d.Name
	}
	cases, err := m.Store.ListCaseRuns(ctx, m.DB, id)
	if err != nil {
		log.WithError(err).Error("training evals: list eval case runs failed")
		return nil, internalErr("failed to read the eval's cases", err)
	}
	out := make([]CaseView, len(cases))
	for i := range cases {
		// The cases of a run are stored in order, batch by batch, so those stored so far are the first ones the run asked.
		out[i] = ViewOfCaseRun(&cases[i], names[cases[i].DefID], i)
	}
	return out, nil
}

// CancelRun stops a running eval (it ends as cancelled, keeping the batches done so far) and returns it as it was. Errors are *Error:
// KindNotFound for an unknown eval, KindConflict when it is not running.
func (m *Manager) CancelRun(ctx context.Context, id int64) (*RunView, error) {
	run, err := m.run0(ctx, core.EvalRunID(id))
	if err != nil {
		return nil, err
	}
	vs, err := m.views(ctx, []core.EvalRun{*run})
	if err != nil {
		return nil, err
	}
	if run.Status != core.StatusRunning && run.Status != core.StatusPending {
		return nil, conflictErr("the eval is not running")
	}
	tm := int64(0)
	if meta, ok := run.Meta.(*core.TrainingEvalRunMeta); ok {
		tm = meta.TrainedModelID
	}
	m.mu.Lock()
	cur := m.running[tm]
	m.mu.Unlock()
	if cur == nil || cur.runID != run.ID {
		// Marked running, but nothing runs it (it cannot happen unless the bookkeeping failed): end it.
		end := time.Now().UTC().Truncate(time.Second)
		run.Status, run.FinishedAt = core.StatusCancelled, &end
		if err := m.Store.UpdateEvalRun(ctx, m.DB, run); err != nil {
			log.WithError(err).Error("training evals: ending the eval failed")
		}
	} else {
		cur.cancel()
	}
	return &vs[0], nil
}

// Watch is an eval as a live feed starts: the run and its cases so far, then Events (nil if nothing is running the eval any more:
// it is as stored).
type Watch struct {
	Run   *RunView
	Cases []CaseView
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

// Watch starts watching eval id: the snapshot (the run and all of its cases so far: the ones stored, and the ones the model
// answered that the judge has not scored yet, as ViewAnswered), and the events that follow while the eval goes on. The caller must
// Close it. Errors are *Error: KindNotFound for an unknown eval, KindInternal.
func (m *Manager) Watch(ctx context.Context, id int64) (*Watch, error) {
	// Subscribe before reading the database, so no event falls between the two; the overlap is harmless: the cases are applied by
	// idx, and the stored version wins.
	m.mu.Lock()
	feed := m.feeds[core.EvalRunID(id)]
	m.mu.Unlock()
	w := &Watch{feed: feed}
	var pending []CaseView
	if feed != nil {
		pending, w.Events = feed.Subscribe()
	}
	fail := func(err error) (*Watch, error) { w.Close(); return nil, err }
	run, err := m.GetRun(ctx, id)
	if err != nil {
		return fail(err)
	}
	cases, err := m.caseViews(ctx, core.EvalRunID(id))
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
	w.Run, w.Cases = run, cases
	return w, nil
}

//---------------------------------------------------------------------------------------------
// Experiments

// ExperimentView is a training experiment: a named series of evals of trained models, to compare trained models (training
// recipes) by their eval scores. It is an experiment def of the evals that has a training eval; its runs are the evals started in it.
type ExperimentView struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Config      map[string]any `json:"config"` // free-form settings under test (the training recipe, the judge, ...)
	CreatedAt   time.Time      `json:"created_at"`
	// NumRuns is how many runs the experiment has.
	NumRuns int `json:"num_runs"`
}

// ExperimentRunView is one run of a training experiment: an eval of one trained model. Eval is the eval as it is now (so a running
// one shows its progress) and Model the trained model that was evaluated.
type ExperimentRunView struct {
	ID           int64     `json:"id"` // the same as Eval.ID: the eval run is the run
	ExperimentID int64     `json:"experiment_id"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	Eval         RunView   `json:"eval"`
	Model        ModelView `json:"model"`
	// MeanScore is the mean judge score (0 to 5) of the judged answers and CorrectRate the share of them that is correct (a score
	// of at least core.TrainingPassScore); both are nil while nothing is judged.
	MeanScore   *float64 `json:"mean_score"`
	CorrectRate *float64 `json:"correct_rate"`
}

// ModelView is what a run says about the trained model that was evaluated: enough to compare runs by training recipe.
type ModelView struct {
	ID            int64           `json:"id"`
	Name          string          `json:"name"`
	BaseModel     string          `json:"base_model"`
	ModelSize     string          `json:"model_size"`
	NumTrainSteps int             `json:"num_train_steps"`
	FinalLoss     *float64        `json:"final_loss"`
	Params        json.RawMessage `json:"params"` // the other training settings (LoRA rank, learning rate, ...)
}

// isTraining reports whether the experiment def has an eval of the training Type.
func (m *Manager) isTraining(ctx context.Context, d *core.ExperimentDef, types map[core.EvalDefID]core.Type) (bool, error) {
	for _, id := range d.EvalDefIDs {
		t, ok := types[id]
		if !ok {
			e, err := m.Store.GetEvalDef(ctx, m.DB, id)
			if err != nil {
				return false, err
			}
			t = e.Type
			types[id] = t
		}
		if t == core.TypeTraining {
			return true, nil
		}
	}
	return false, nil
}

func experimentView(d *core.ExperimentDef, runs int) ExperimentView {
	v := ExperimentView{ID: int64(d.ID), Name: d.Name, Description: d.Description, Config: map[string]any(d.Params), NumRuns: runs, CreatedAt: d.CreatedAt}
	if v.Config == nil {
		v.Config = map[string]any{}
	}
	return v
}

// ListExperiments returns the training experiments, newest first. Errors are *Error: KindInternal.
func (m *Manager) ListExperiments(ctx context.Context) ([]ExperimentView, error) {
	defs, err := m.Store.ListExperimentDefs(ctx, m.DB)
	if err != nil {
		log.WithError(err).Error("training evals: list experiments failed")
		return nil, internalErr("failed to list the experiments", err)
	}
	types := map[core.EvalDefID]core.Type{}
	out := []ExperimentView{}
	for i := range defs {
		ok, err := m.isTraining(ctx, &defs[i], types)
		if err == nil && ok {
			var runs []core.ExperimentRun
			if runs, err = m.Store.ListExperimentRuns(ctx, m.DB, defs[i].ID); err == nil {
				out = append(out, experimentView(&defs[i], len(runs)))
			}
		}
		if err != nil {
			log.WithError(err).Error("training evals: list experiments failed")
			return nil, internalErr("failed to list the experiments", err)
		}
	}
	slices.Reverse(out)
	return out, nil
}

// GetExperiment returns training experiment id with its runs, oldest first. Errors are *Error: KindNotFound for an unknown
// experiment, KindInternal.
func (m *Manager) GetExperiment(ctx context.Context, id int64) (*ExperimentView, []ExperimentRunView, error) {
	def, err := m.Store.GetExperimentDef(ctx, m.DB, core.ExperimentDefID(id))
	if err == nil {
		var ok bool
		if ok, err = m.isTraining(ctx, def, map[core.EvalDefID]core.Type{}); err == nil && !ok {
			err = db.ErrNotFound
		}
	}
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil, notFoundErr("unknown experiment")
	} else if err != nil {
		log.WithError(err).Error("training evals: read experiment failed")
		return nil, nil, internalErr("failed to read the experiment", err)
	}
	xruns, err := m.Store.ListExperimentRuns(ctx, m.DB, def.ID)
	var evals []core.EvalRun
	if err == nil {
		evals, err = m.Store.ListEvalRunsOfExperiment(ctx, m.DB, def.ID)
	}
	if err != nil {
		log.WithError(err).Error("training evals: list experiment runs failed")
		return nil, nil, internalErr("failed to list the experiment's runs", err)
	}
	evals = slices.DeleteFunc(evals, func(e core.EvalRun) bool { return e.Type != core.TypeTraining })
	views, err := m.views(ctx, evals)
	if err != nil {
		return nil, nil, err
	}
	models := map[int64]ModelView{}
	runs := make([]ExperimentRunView, len(evals))
	for i, ev := range views {
		mv, ok := models[ev.TrainedModelID]
		if !ok {
			mv = ModelView{ID: ev.TrainedModelID, Params: json.RawMessage("{}")}
			if tm, err := trainingdb.GetTrainedModel(ctx, m.DB, ev.TrainedModelID); err == nil {
				mv = ModelView{ID: tm.ID, Name: tm.Name, BaseModel: tm.BaseModel, ModelSize: tm.ModelSize, NumTrainSteps: tm.NumTrainSteps, FinalLoss: tm.FinalLoss, Params: tm.Params}
				if len(mv.Params) == 0 {
					mv.Params = json.RawMessage("{}")
				}
			} else if !errors.Is(err, relational.ErrNotFound) {
				log.WithError(err).Warn("training evals: read the trained model of a run failed")
			}
			models[ev.TrainedModelID] = mv
		}
		runs[i] = ExperimentRunView{ID: ev.ID, ExperimentID: int64(def.ID), Status: ev.Status,
			CreatedAt: ev.CreatedAt, Eval: ev, Model: mv, MeanScore: ev.MeanScore(), CorrectRate: ev.CorrectRate()}
	}
	v := experimentView(def, len(xruns))
	return &v, runs, nil
}
