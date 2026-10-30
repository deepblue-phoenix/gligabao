package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
)

// ExperimentRecorder records one run of an experiment: the run is stored as it starts, and rewritten as the eval runs under it
// end. Start the eval runs with EvalOptions.ExperimentRunID set to Run().ID.
type ExperimentRecorder struct {
	r   *Runner
	run *core.ExperimentRun
}

// StartExperimentRun records a run of the experiment def, running. params are how it is invoked (stored with it).
func (r *Runner) StartExperimentRun(ctx context.Context, def *core.ExperimentDef, params core.Params) (*ExperimentRecorder, error) {
	start := now()
	run := &core.ExperimentRun{DefID: def.ID, Status: core.StatusRunning, Params: params, Timing: core.Timing{CreatedAt: start, StartedAt: &start}}
	if err := r.Store.CreateExperimentRun(ctx, r.DB, run); err != nil {
		return nil, err
	}
	return &ExperimentRecorder{r: r, run: run}, nil
}

// Run is the experiment run being recorded.
func (x *ExperimentRecorder) Run() *core.ExperimentRun { return x.run }

// Add adds an eval run that has ended to the experiment run: its Counts and cost are summed into the run's, which is stored. Total
// is the number of cases of all the eval runs added.
func (x *ExperimentRecorder) Add(ctx context.Context, er *core.EvalRun) error {
	c, e := &x.run.Counts, er.Counts
	c.Total += e.Total
	c.Pending += e.Pending
	c.Running += e.Running
	c.Completed += e.Completed
	c.Errored += e.Errored
	c.Cancelled += e.Cancelled
	c.Passed += e.Passed
	c.Failed += e.Failed
	x.run.CostUSD += er.CostUSD
	return x.r.Store.UpdateExperimentRun(ctx, x.r.DB, x.run)
}

// Finish ends the experiment run: cancelled if err is a cancellation or ctx ended, errored (with err's text) for another error,
// completed if err is nil. It is stored.
func (x *ExperimentRecorder) Finish(ctx context.Context, err error) error {
	end := now()
	x.run.FinishedAt = &end
	switch {
	case err == nil:
		x.run.Status = core.StatusCompleted
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		x.run.Status = core.StatusCancelled
	default:
		x.run.Status, x.run.Error = core.StatusErrored, err.Error()
	}
	if updateErr := x.r.Store.UpdateExperimentRun(context.WithoutCancel(ctx), x.r.DB, x.run); updateErr != nil {
		return fmt.Errorf("end the experiment run: %w", updateErr)
	}
	return nil
}

// ExperimentName is the name of the experiment of a run started from a command line. A name that was given (set, even if empty: no
// experiment) is kept. Otherwise a run that picks one eval (eval is its name) is named after it, since it is targeted, and any other
// run (eval is empty: all the evals, or several) gets the timestamp of its start (now), so that it is unique and not a shared name.
func ExperimentName(set bool, given, eval string, now time.Time) string {
	switch {
	case set:
		return given
	case eval != "":
		return eval
	default:
		return now.Format("2006-01-02_15-04-05")
	}
}
