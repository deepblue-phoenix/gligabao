package memory_remember

// The remember run log: the row of gb_remember_runs (see internal/db/relational/sqlite/db_schema.go) that records how one
// call of Remember was executed, the authoritative record of it: the status of the call and of each stage of the
// pipeline, the error of a stage that failed, when each stage began and ended, and the inferences (gb_inferences) each
// stage made.
//
// The pipeline (see rememberPipeline, and memory_pipeline) reports to it around every stage, and the stage itself has nothing to
// do with it but to file its outputs:
//
//	run := startRunLog(ctx, d, 0, userID, dataset, kind, params) // a row, status started
//	defer func() { run.Finish(err) }()                        // status succeeded or failed (and the stages left open)
//
//	run.Start(StageExtractGraph)                              // before a stage
//	run.Done(err, StageExtractGraph)                          // after it, with its error (nil if it had none)
//	run.Skip(StageWebSearch)                                  // a stage the call does not use
//	run.AddInference(StageExtractGraph, id)                   // the inference (gb_inferences) the stage made
//
// A stage also files what its steps produced, for debugging, in gb_remember_run_stage_outputs (see Output):
//
//	j.run.Output(StageExtractGraph, "extract_graph", "chunk 2 of 5", map[string]any{"entities": ex.Entities, ...})
//
// Every method is safe to call on a nil *runLog (it does nothing). Nothing here ever fails Remember: a row that cannot
// be written is logged, and the call goes on. Every change is written as it happens, so a run that is in progress can be
// watched, and one that dies leaves its last known state (status started).
//
// The web_search, fetch_series and observe_series stages extract and store texts of their own (the briefings, the
// observations) with the same code as the main text. The job that does it gets a view of the log made by Within, which
// takes no part in the stages of the text itself (Start and Done do nothing) and files every inference it is told of in
// the enrichment stage, so that they are not mixed up with those of extract_graph, resolve_edges and store.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"

	log "github.com/sirupsen/logrus"
)

// runLog is the log of one Remember call. See the top of this file.
type runLog struct {
	*runState
	// into, if set, makes this a view (see Within): it does not track stages, and files inferences in this stage.
	into string
}

// runState is what the views of a log share.
type runState struct {
	d *sql.DB
	// ctx is what the writes use: the context of the call, but not cancelled when it is, so that the failure of a cancelled
	// call is recorded too.
	ctx context.Context

	mu  sync.Mutex // guards run, and serializes the writes so that the last state wins
	run relational.RememberRun
}

// The stages of the remember pipeline (see relational.RememberStages) that are not LLM stages of memory_core. The LLM stages
// are named by memory_core (StageExtractGraph, StageSummarizeChunk, StageResolveEdges, StageWebSearch), and
// StageClassifyComplexity is in classify.go.
const (
	StageChunkText      = relational.RememberStageChunkText
	StageLoadCandidates = relational.RememberStageLoadCandidates
	StageStore          = relational.RememberStageStore
	StageStoreSeries    = relational.RememberStageStoreSeries
	StageFetchSeries    = relational.RememberStageFetchSeries
	StageResolveGeo     = relational.RememberStageResolveGeo
	StageObserveSeries  = relational.RememberStageObserveSeries
)

// startRunLog records the start of a Remember call (status started, all the stages not started) and returns its log.
// params is how the call was made. If the row cannot be written the log is kept in memory only (and the error logged).
//
// With adopt not 0 the row with that ID (made by the remember manager when the request was queued, see manager.go) is
// used instead of creating one: it is overwritten with the run that starts now.
func startRunLog(ctx context.Context, d *sql.DB, adopt int64, userID, dataset, kind string, params map[string]any) *runLog {
	r := &runLog{runState: &runState{d: d, ctx: context.WithoutCancel(ctx), run: relational.NewRememberRun(userID, dataset, kind, params, relational.Now())}}
	if adopt != 0 {
		r.run.ID = adopt
		r.mu.Lock()
		r.save()
		r.mu.Unlock()
		return r
	}
	id, err := relational.CreateRememberRun(r.ctx, d, r.run)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{"user_id": userID, "dataset": dataset}).Error("remember run log: create")
		return r
	}
	r.run.ID = id
	return r
}

// ID is the ID of the row in gb_remember_runs; 0 if it could not be created.
func (r *runLog) ID() int64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.run.ID
}

// save writes the log. The caller holds r.mu.
func (r *runLog) save() {
	if r.run.ID == 0 {
		return
	}
	if err := relational.SaveRememberRun(r.ctx, r.d, r.run); err != nil {
		log.WithError(err).WithField("remember_run_id", r.run.ID).Error("remember run log: save")
	}
}

// stage returns the stage name, or nil (logging it) if the pipeline has no such stage. The caller holds r.mu.
func (r *runLog) stage(name string) *relational.RememberStage {
	st, ok := r.run.Stages[name]
	if !ok {
		log.WithField("stage", name).Error("remember run log: unknown stage")
		return nil
	}
	return &st
}

// Start marks the stages as started now. A stage that is started again (it runs for each chunk of a text) keeps the start of
// its first run. Call it before the stage.
func (r *runLog) Start(stages ...string) {
	if r == nil || r.into != "" {
		return
	}
	now := relational.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range stages {
		if st := r.stage(name); st != nil {
			st.Status = relational.RememberStarted
			if st.StartedAt == nil {
				st.StartedAt = &now
			}
			r.run.Stages[name] = *st
		}
	}
	r.save()
}

// Skip marks the stages as skipped: the call does not use them (see relational.RememberSkipped). A stage that has begun is left
// as it is, whatever it did (the storage of the series is done by store when the text is a single chunk). Call it when the
// pipeline passes the stage.
func (r *runLog) Skip(stages ...string) {
	if r == nil || r.into != "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range stages {
		if st := r.stage(name); st != nil && st.Status == relational.RememberNotStarted {
			st.Status = relational.RememberSkipped
			r.run.Stages[name] = *st
		}
	}
	r.save()
}

// Done marks the stages as ended now: failed with err's message if err is not nil, else succeeded. A stage that failed stays
// failed. Call it after the stage, with the error it returned (nil if it had none).
func (r *runLog) Done(err error, stages ...string) {
	if r == nil || r.into != "" {
		return
	}
	now := relational.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range stages {
		if st := r.stage(name); st != nil {
			end(st, err, now)
			r.run.Stages[name] = *st
		}
	}
	r.save()
}

// end finishes st at now (see Done).
func end(st *relational.RememberStage, err error, now time.Time) {
	switch {
	case st.Status == relational.RememberFailed:
	case err != nil:
		msg := err.Error()
		st.Status, st.Error = relational.RememberFailed, &msg
	default:
		st.Status = relational.RememberSucceeded
	}
	st.FinishedAt = &now
}

// Artifact records the artifact the text was stored as (the parent, for a chunked text).
func (r *runLog) Artifact(id int64) {
	if r == nil || r.into != "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.run.ArtifactID = &id
	r.save()
}

// Finish ends the call: failed with err's message if err is not nil, else succeeded. A stage still started is ended with it
// (failed with the same error, or succeeded), so no run is left with a stage that is open. Call it once, when Remember
// returns.
func (r *runLog) Finish(err error) {
	if r == nil {
		return
	}
	now := relational.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, st := range r.run.Stages {
		if st.Status == relational.RememberStarted {
			end(&st, err, now)
			r.run.Stages[name] = st
		}
	}
	r.run.FinishedAt = &now
	if err != nil {
		msg := err.Error()
		r.run.Status, r.run.Error = relational.RememberFailed, &msg
	} else {
		r.run.Status = relational.RememberSucceeded
	}
	r.save()
}

// AddInference adds the inference with that ID (gb_inferences) to the stage that made it. Call it wherever a model is run
// (an LLM call, an embedding request, a web search), with the ID that recording it returned; 0 (a run that could not be
// recorded) is ignored. The stage must be one that runs a model (relational.RememberStages).
func (r *runLog) AddInference(stage string, id int64) {
	if r == nil || id == 0 {
		return
	}
	if r.into != "" {
		stage = r.into
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.stage(stage)
	if st == nil {
		return
	}
	if def, _ := relational.RememberStageDefOf(stage); !def.Model {
		log.WithField("stage", stage).Error("remember run log: the stage runs no model")
		return
	}
	st.InferenceIDs = append(st.InferenceIDs, id)
	r.run.Stages[stage] = *st
	r.save()
}

// maxStageOutputBytes is the largest output kept whole; a larger one is replaced by a stub (see Output).
const maxStageOutputBytes = 64 << 10

// Output files an output of a step of the stage, for debugging (gb_remember_run_stage_outputs): v, which is marshaled to JSON.
// name is the step (extract_graph, resolve_edges, ...); label says what the output is about when the step runs more than
// once (a chunk, an entity), "" if not. Outputs are kept in the order they are filed. Call it outside any open transaction,
// after the step (a failed step files what it has: the raw LLM output that could not be parsed, ...).
//
// The stage is the one the output belongs to; in a view made by Within every output is filed in the view's stage, whatever
// stage it names, and the name keeps what the step was. An output of more than maxStageOutputBytes is replaced by
// {"truncated": true, "bytes": n, "preview": the start of it}. Like everything here it never fails Remember.
func (r *runLog) Output(stage, name, label string, v any) {
	if r == nil {
		return
	}
	if r.into != "" {
		stage = r.into
	}
	if _, ok := relational.RememberStageDefOf(stage); !ok {
		log.WithField("stage", stage).Error("remember run log: unknown stage")
		return
	}
	id := r.ID()
	if id == 0 {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{"remember_run_id": id, "stage": stage, "name": name}).Error("remember run log: marshal output")
		b, _ = json.Marshal(map[string]any{"error": "the output could not be marshaled: " + err.Error()})
	}
	if len(b) > maxStageOutputBytes {
		b, _ = json.Marshal(map[string]any{"truncated": true, "bytes": len(b), "preview": strings.ToValidUTF8(string(b[:4096]), "")})
	}
	o := relational.RememberStageOutput{RunID: id, Stage: stage, Name: name, Label: label, Output: b}
	if _, err := relational.AddRememberStageOutput(r.ctx, r.d, o); err != nil {
		log.WithError(err).WithFields(log.Fields{"remember_run_id": id, "stage": stage, "name": name}).Error("remember run log: add output")
	}
}

// Within returns a view of the log for the job that stores the texts of an enrichment stage (see the top of this file): its
// Start, Done and Artifact do nothing, and the inferences it is told of are added to stage, whatever stage they name.
func (r *runLog) Within(stage string) *runLog {
	if r == nil {
		return nil
	}
	return &runLog{runState: r.runState, into: stage}
}

//---------------------------------------------------------------------------------------------

// resultError is a result of an enrichment stage, which carries its error as text ("" if it had none).
type resultError interface{ errText() string }

func (r WebSearchResult) errText() string         { return r.Error }
func (r SeriesFetchResult) errText() string       { return r.Error }
func (r SeriesObservationResult) errText() string { return r.Error }

// resultsError is the error of an enrichment stage that does not fail the call: the first error of its results, nil if
// none has one.
func resultsError[T resultError](results []T) error {
	for _, r := range results {
		if msg := r.errText(); msg != "" {
			return errors.New(msg)
		}
	}
	return nil
}
