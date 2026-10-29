// Package memory_pipeline is how the pipelines of the memory operations (memory_remember.Remember, memory_query.Query) declare
// and run their stages.
//
// A pipeline is a list of steps, a value that reads top to bottom as the stages of the operation do. Each step is a Stage: a
// name, what happens if it fails (Policy), when it is used (Enabled) and the function that does the work. The work on a
// stage does not tell the run log about it: Run does, around every stage, so that a stage cannot be left open, cannot be
// forgotten and cannot be named wrong:
//
//	var pipeline = []memory_pipeline.Step[job]{
//		memory_pipeline.Stage[job]{Name: "extract", Run: (*job).extract}, // func (j *job) extract(ctx context.Context) error
//		memory_pipeline.Stage[job]{Name: "store", Run: (*job).store},
//		memory_pipeline.Stage[job]{Name: "geo", Policy: memory_pipeline.Advisory, Enabled: (*job).hasGeo, Run: (*job).geo},
//	}
//	err := memory_pipeline.Run(ctx, pipeline, j, observer)
//
// For every stage Run tells the Observer (the run log of the call: gb_remember_runs, gb_query_runs):
//   - Start before it, and Done after it with its error;
//   - Skip if it is not Enabled (the call does not use it), or if an earlier stage ended the pipeline with Stop;
//   - the inferences it made (gb_inferences): the context the stage runs with carries an inference sink (see
//     memory_core.WithInferenceSink), so every model call, embedding request or web search that the stage makes with that
//     context is filed under it, with no call to the log.
//
// The stages share state through S, the value the pipeline runs on (the job of the call): a stage reads what the earlier ones
// left in it and leaves what the later ones need.
package memory_pipeline

import (
	"context"
	"errors"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// Policy says what happens to the pipeline when a stage returns an error.
type Policy int

const (
	// Fatal: the error ends the pipeline, and Run returns it. The zero value.
	Fatal Policy = iota
	// Advisory: the error is recorded on the stage (Done with it) and the pipeline goes on: the stage is not needed for the
	// call to succeed (geo, web search, ...).
	Advisory
)

// Stop is returned by a stage to end the pipeline early without an error (there is nothing left to do, a question with no
// information to answer from). The stage ends as succeeded, the stages after it are skipped, and Run returns nil.
var Stop = errors.New("pipeline: stop")

// Observer is what Run tells of the stages: the run log of the call. Every stage is named as in the log. The calls of one
// Run are made by the goroutine that called it, except AddInference, which may come from any of the goroutines of a stage.
type Observer interface {
	// Start says that the stages began now. A stage that starts again (it is run for each chunk) keeps its first start.
	Start(stages ...string)
	// Done says that the stages ended now, failed with err if it is not nil.
	Done(err error, stages ...string)
	// Skip says that the stages are not used by this call. A stage that has begun is left as it is.
	Skip(stages ...string)
	// AddInference says that the inference (gb_inferences) with that ID was made by the stage.
	AddInference(stage string, id int64)
}

// Step is a Stage, or an Each of stages.
type Step[S any] interface {
	exec(ctx context.Context, r *runner[S], s *S) error
	// names appends the names of the stages of the step, in order.
	names(into []string) []string
}

// Stage is one stage of a pipeline.
type Stage[S any] struct {
	// Name is the stage, as the run log names it.
	Name   string
	Policy Policy
	// Enabled says whether the call uses the stage; nil means always. A stage that is not enabled is skipped, and Run is not
	// called.
	Enabled func(*S) bool
	// Also names the stages that this one does too, when it does (for instance the storage of the series, which happens in
	// the transaction that stores the text): they start and end with it. nil means none.
	Also func(*S) []string
	// Run does the work of the stage. It returns the error that ends it (see Policy, and Stop). The state comes first, so that
	// a method of it can be Run as it is: (*job).extract.
	Run func(s *S, ctx context.Context) error
}

// Each runs its steps once for every item of the call (every chunk of a document), in order and each item to the end, before
// the next one. It is not a stage of the run log: the stages of Steps are, and they start again for each item. Count is how
// many items there are, and Set makes the item i the current one of s.
type Each[S any] struct {
	Count func(*S) int
	Set   func(s *S, i int)
	Steps []Step[S]
}

// Run runs the steps on s, in order, telling obs. It returns the error of the first Fatal stage that failed, after which no
// other stage runs (they stay as they were: not started); nil if none did, or if a stage ended the pipeline with Stop. obs may
// be nil.
func Run[S any](ctx context.Context, steps []Step[S], s *S, obs Observer) error {
	r := &runner[S]{obs: obs}
	for _, st := range steps {
		if err := st.exec(ctx, r, s); err != nil {
			return err
		}
	}
	return nil
}

// Names returns the names of the stages of the steps, in order, each once (at its first place).
func Names[S any](steps []Step[S]) []string {
	var all []string
	for _, st := range steps {
		all = st.names(all)
	}
	seen := map[string]bool{}
	out := all[:0:0]
	for _, n := range all {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

type runner[S any] struct {
	obs     Observer
	stopped bool // a stage returned Stop: the rest is skipped
}

func (r *runner[S]) skip(names ...string) {
	if r.obs != nil && len(names) > 0 {
		r.obs.Skip(names...)
	}
}

func (st Stage[S]) names(into []string) []string { return append(into, st.Name) }

func (st Stage[S]) exec(ctx context.Context, r *runner[S], s *S) error {
	if r.stopped || (st.Enabled != nil && !st.Enabled(s)) {
		r.skip(st.Name)
		return nil
	}
	names := []string{st.Name}
	if st.Also != nil {
		names = append(names, st.Also(s)...)
	}
	if r.obs != nil {
		r.obs.Start(names...)
		obs, name := r.obs, st.Name
		ctx = memory_core.WithInferenceSink(ctx, func(id int64) { obs.AddInference(name, id) })
	}
	err := st.Run(s, ctx)
	stop := errors.Is(err, Stop)
	if stop {
		err = nil
	}
	if r.obs != nil {
		r.obs.Done(err, names...)
	}
	if stop {
		r.stopped = true
		return nil
	}
	if err != nil && st.Policy == Fatal {
		return err
	}
	return nil
}

func (e Each[S]) names(into []string) []string {
	for _, st := range e.Steps {
		into = st.names(into)
	}
	return into
}

func (e Each[S]) exec(ctx context.Context, r *runner[S], s *S) error {
	if r.stopped {
		r.skip(e.names(nil)...)
		return nil
	}
	for i, n := 0, e.Count(s); i < n; i++ {
		e.Set(s, i)
		for _, st := range e.Steps {
			if err := st.exec(ctx, r, s); err != nil {
				return err
			}
		}
		if r.stopped {
			return nil
		}
	}
	return nil
}
