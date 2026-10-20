// Package runner is what every Type of eval shares in running: it puts the evals that come from files into the database as defs
// (SyncEval, EnsureExperiment) and records the runs (ExperimentRecorder, EvalRecorder) while an executor of the eval's Type does the
// work of the cases. The executors are in the packages of the Types (internal/evals/knowledge, .../training); they know
// nothing of the database beyond the recorder they are given.
//
// A run is recorded as it goes: the eval run is stored as soon as it starts and rewritten as its cases finish, so a run that is
// still going shows its progress, and one that is killed stays "running" (nothing here fixes that).
package runner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/db"
)

// Runner records evals in a database.
type Runner struct {
	DB    *sql.DB
	Store db.Store
}

// New returns a Runner for d, a database with the schema applied (see evals.Open), whose backend is the one of store.
func New(d *sql.DB, store db.Store) *Runner { return &Runner{DB: d, Store: store} }

// now is the current time in the precision of the storage of every backend.
func now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// tx runs f in a transaction: committed if it returns nil, rolled back otherwise.
func (r *Runner) tx(ctx context.Context, f func(q db.Execer) error) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Executor runs the cases of an eval of one Type. Execute runs def.Cases (the ones selected; not necessarily all of the eval's), in
// order, and records what happens through rec: a case run for each case it reaches (StartCase and FinishCase, or RecordCases). A
// case that fails is recorded as errored and the run goes on, unless it cannot go on (the model is down): then Execute returns
// an error and the eval run is errored. When ctx ends it returns the error of ctx and the eval run is cancelled.
type Executor interface {
	Execute(ctx context.Context, rec *EvalRecorder, def *core.EvalDef) error
}

// EvalOptions say how an eval run is started.
type EvalOptions struct {
	// ExperimentRunID is the experiment run it is part of; nil to run the eval on its own.
	ExperimentRunID *core.ExperimentRunID
	// Params are how the run was invoked, stored with it.
	Params core.Params
	// Meta is the initial metadata of the eval run, of the Type of the eval (for example a *core.TrainingEvalRunMeta with the
	// trained model). Its EvalRunID is set. Nil for none.
	Meta core.EvalRunMeta
	// OnCreated, if set, is called with the eval run once it is recorded (running, with its ID), before the executor starts. The
	// run is the one the recorder keeps: it changes as the cases finish, so a callback that keeps it must read it on the goroutine
	// of RunEval (the executor's callbacks do) or copy it.
	OnCreated func(run *core.EvalRun)
}

// RunEval runs the cases of def (def.Cases; the caller selects them) with ex as one eval run: it records the eval run, calls
// ex.Execute, and ends the run completed, errored (ex failed) or cancelled (ctx ended). It returns the eval run as it was left,
// and the error of ex if it had one; an error of the database is returned as it is (and the run may be left running).
func (r *Runner) RunEval(ctx context.Context, def *core.EvalDef, ex Executor, opts EvalOptions) (*core.EvalRun, error) {
	start := now()
	run := &core.EvalRun{
		DefID: def.ID, Type: def.Type, ExperimentRunID: opts.ExperimentRunID, Status: core.StatusRunning, Params: opts.Params,
		Counts: core.Counts{Total: len(def.Cases), Pending: len(def.Cases)},
		Timing: core.Timing{CreatedAt: start, StartedAt: &start},
		Meta:   opts.Meta,
	}
	err := r.tx(ctx, func(q db.Execer) error {
		if err := r.Store.CreateEvalRun(ctx, q, run); err != nil {
			return err
		}
		if run.Meta == nil {
			return nil
		}
		setEvalRunID(run.Meta, run.ID)
		return r.Store.CreateEvalRunMeta(ctx, q, run.Meta)
	})
	if err != nil {
		return nil, err
	}
	if opts.OnCreated != nil {
		opts.OnCreated(run)
	}
	rec := &EvalRecorder{r: r, run: run}
	execErr := ex.Execute(ctx, rec, def)
	end := now()
	run.FinishedAt = &end
	switch {
	case execErr == nil:
		run.Status = core.StatusCompleted
	case ctx.Err() != nil || errors.Is(execErr, context.Canceled):
		run.Status = core.StatusCancelled
	default:
		run.Status, run.Error = core.StatusErrored, execErr.Error()
	}
	if err := rec.save(context.WithoutCancel(ctx), r.DB); err != nil {
		return run, errors.Join(execErr, fmt.Errorf("end the eval run: %w", err))
	}
	return run, execErr
}

// setEvalRunID sets the EvalRunID of a metadata struct that the caller gave by pointer (a value cannot be set; the run's
// metadata must be a pointer for the executor to update it).
func setEvalRunID(m core.EvalRunMeta, id core.EvalRunID) {
	switch m := m.(type) {
	case *core.KnowledgeEvalRunMeta:
		m.EvalRunID = id
	case *core.TrainingEvalRunMeta:
		m.EvalRunID = id
	}
}

func setCaseRunID(m core.CaseRunMeta, id core.CaseRunID) {
	switch m := m.(type) {
	case *core.KnowledgeCaseRunMeta:
		m.CaseRunID = id
	case *core.TrainingCaseRunMeta:
		m.CaseRunID = id
	}
}

// EvalRecorder records the case runs of one eval run, and keeps the eval run's Counts, CostUSD and Meta up to date in the database
// as they finish. An Executor gets it from RunEval.
type EvalRecorder struct {
	r   *Runner
	run *core.EvalRun
}

// Run is the eval run being recorded. The executor may change its Meta (a pointer to the struct of its Type) and its CostUSD; they are
// stored with the next case run it records.
func (e *EvalRecorder) Run() *core.EvalRun { return e.run }

// save rewrites the eval run and its Meta.
func (e *EvalRecorder) save(ctx context.Context, q db.Execer) error {
	if err := e.r.Store.UpdateEvalRun(ctx, q, e.run); err != nil {
		return err
	}
	if e.run.Meta != nil {
		return e.r.Store.UpdateEvalRunMeta(ctx, q, e.run.Meta)
	}
	return nil
}

// Save stores the eval run as it is now (its Counts, CostUSD and Meta) without recording a case run.
func (e *EvalRecorder) Save(ctx context.Context) error { return e.save(ctx, e.r.DB) }

// StartCase records that the case def starts: a case run, running. Pass it to FinishCase when the case is over.
func (e *EvalRecorder) StartCase(ctx context.Context, def core.CaseDef) (*core.CaseRun, error) {
	start := now()
	cr := &core.CaseRun{DefID: def.ID, EvalRunID: e.run.ID, Type: def.Type, Status: core.StatusRunning, Timing: core.Timing{CreatedAt: start, StartedAt: &start}}
	e.run.Counts.Pending--
	e.run.Counts.Running++
	err := e.r.tx(ctx, func(q db.Execer) error {
		if err := e.r.Store.CreateCaseRun(ctx, q, cr); err != nil {
			return err
		}
		return e.r.Store.UpdateEvalRun(ctx, q, e.run)
	})
	if err != nil {
		e.run.Counts.Pending++
		e.run.Counts.Running--
		return nil, err
	}
	return cr, nil
}

// FinishCase records the end of a case run that StartCase made. The caller has set its Status (completed, errored or cancelled; a
// case that did not run to the end has no verdict or score), Error, Verdict, Score and CostUSD. meta is the metadata of its Type,
// stored with it (nil for none); its case run ID is set here. The eval run is rewritten with its Counts and cost updated.
func (e *EvalRecorder) FinishCase(ctx context.Context, cr *core.CaseRun, meta core.CaseRunMeta) error {
	if !cr.Status.Finished() {
		return fmt.Errorf("finish case run %d: status %q is not final", cr.ID, cr.Status)
	}
	end := now()
	cr.FinishedAt = &end
	counts, cost := e.run.Counts, e.run.CostUSD
	e.run.Counts.Running--
	e.count(cr)
	err := e.r.tx(ctx, func(q db.Execer) error {
		if err := e.r.Store.UpdateCaseRun(ctx, q, cr); err != nil {
			return err
		}
		if meta != nil {
			setCaseRunID(meta, cr.ID)
			if err := e.r.Store.CreateCaseRunMeta(ctx, q, meta); err != nil {
				return err
			}
		}
		return e.save(ctx, q)
	})
	if err != nil {
		e.run.Counts, e.run.CostUSD = counts, cost
	}
	return err
}

// CaseRecord is a case run to be recorded in one go, already final: its def, outcome and metadata.
type CaseRecord struct {
	// Run has the DefID, Type, Status, Error, Verdict, Score, CostUSD and times of the case run; its EvalRunID is set by
	// RecordCases.
	Run  *core.CaseRun
	Meta core.CaseRunMeta
}

// RecordCases records case runs that were not started one by one (the executor did the cases in a batch): each is created already
// final, with its metadata, in one transaction with the rewrite of the eval run, whose Counts and cost are updated by them.
func (e *EvalRecorder) RecordCases(ctx context.Context, cases []CaseRecord) error {
	counts, cost := e.run.Counts, e.run.CostUSD
	for _, c := range cases {
		if !c.Run.Status.Finished() {
			return fmt.Errorf("record case run of case def %d: status %q is not final", c.Run.DefID, c.Run.Status)
		}
		e.run.Counts.Pending--
		e.count(c.Run)
	}
	err := e.r.tx(ctx, func(q db.Execer) error {
		for _, c := range cases {
			c.Run.EvalRunID = e.run.ID
			if c.Run.FinishedAt == nil {
				end := now()
				c.Run.FinishedAt = &end
			}
			if err := e.r.Store.CreateCaseRun(ctx, q, c.Run); err != nil {
				return err
			}
			if c.Meta != nil {
				setCaseRunID(c.Meta, c.Run.ID)
				if err := e.r.Store.CreateCaseRunMeta(ctx, q, c.Meta); err != nil {
					return err
				}
			}
		}
		return e.save(ctx, q)
	})
	if err != nil {
		e.run.Counts, e.run.CostUSD = counts, cost
	}
	return err
}

// count adds a finished case run to the eval run's Counts and cost.
func (e *EvalRecorder) count(cr *core.CaseRun) {
	c := &e.run.Counts
	switch cr.Status {
	case core.StatusCompleted:
		c.Completed++
		switch cr.Verdict {
		case core.VerdictPass:
			c.Passed++
		case core.VerdictFail:
			c.Failed++
		}
	case core.StatusErrored:
		c.Errored++
	case core.StatusCancelled:
		c.Cancelled++
	}
	e.run.CostUSD += cr.CostUSD
}
