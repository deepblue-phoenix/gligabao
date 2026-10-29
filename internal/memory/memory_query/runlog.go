package memory_query

// The query run log: the row of gb_query_runs (see internal/db/relational/sqlite/db_schema.go) that records how one call of
// Query was executed, the authoritative record of it, like the remember run log (see memory_remember/runlog.go): the status of
// the call and of each stage of the pipeline, the error of a stage that failed, when each stage began and ended, and the
// inferences (gb_inferences) each stage made.
//
// The pipeline (see Query, and memory_pipeline) is what reports to it, around every stage, so that no stage has to:
//
//	run := startRunLog(ctx, d, userID, dataset, question, sessionID, params) // a row, status started
//	defer func() { run.Finish(err) }()                                        // status succeeded or failed (and the stages left open)
//
// Every method is safe to call on a nil *runLog (it does nothing). Nothing here ever fails Query: a row that cannot be written is
// logged, and the call goes on. Every change is written as it happens, so a run that is in progress can be watched, and one
// that dies leaves its last known state (status started).

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"

	log "github.com/sirupsen/logrus"
)

// runLog is the log of one Query call. See the top of this file.
type runLog struct {
	d *sql.DB
	// ctx is what the writes use: the context of the call, but not cancelled when it is, so that the failure of a cancelled
	// call is recorded too.
	ctx context.Context

	mu  sync.Mutex // guards run, and serializes the writes so that the last state wins
	run relational.QueryRun
}

// startRunLog records the start of a Query call (status started, all the stages not started) and returns its log. params is how
// the call was made. If the row cannot be written the log is kept in memory only (and the error logged).
func startRunLog(ctx context.Context, d *sql.DB, userID, dataset, question, sessionID string, params map[string]any) *runLog {
	r := &runLog{d: d, ctx: context.WithoutCancel(ctx), run: relational.NewQueryRun(userID, dataset, question, sessionID, params, relational.Now())}
	id, err := relational.CreateQueryRun(r.ctx, d, r.run)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{"user_id": userID, "dataset": dataset}).Error("query run log: create")
		return r
	}
	r.run.ID = id
	return r
}

// ID is the ID of the row in gb_query_runs; 0 if it could not be created.
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
	if err := relational.SaveQueryRun(r.ctx, r.d, r.run); err != nil {
		log.WithError(err).WithField("query_run_id", r.run.ID).Error("query run log: save")
	}
}

// stage returns the stage name, or nil (logging it) if the pipeline has no such stage. The caller holds r.mu.
func (r *runLog) stage(name string) *relational.QueryStage {
	st, ok := r.run.Stages[name]
	if !ok {
		log.WithField("stage", name).Error("query run log: unknown stage")
		return nil
	}
	return &st
}

// Start marks the stages as started now.
func (r *runLog) Start(stages ...string) {
	if r == nil {
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

// Done marks the stages as ended now: failed with err's message if err is not nil, else succeeded.
func (r *runLog) Done(err error, stages ...string) {
	if r == nil {
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

// Skip marks the stages as skipped: the call does not use them. A stage that has begun is left as it is.
func (r *runLog) Skip(stages ...string) {
	if r == nil {
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

// end finishes st at now (see Done).
func end(st *relational.QueryStage, err error, now time.Time) {
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

// AddInference adds the inference with that ID (gb_inferences) to the stage that made it; 0 (a run that could not be recorded)
// is ignored. The stage must be one that runs a model (relational.QueryStages).
func (r *runLog) AddInference(stage string, id int64) {
	if r == nil || id == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.stage(stage)
	if st == nil {
		return
	}
	if !runsModel(stage) {
		log.WithField("stage", stage).Error("query run log: the stage runs no model")
		return
	}
	st.InferenceIDs = append(st.InferenceIDs, id)
	r.run.Stages[stage] = *st
	r.save()
}

func runsModel(stage string) bool {
	for _, s := range relational.QueryStages {
		if s.Name == stage {
			return s.Model
		}
	}
	return false
}

// Finish ends the call: failed with err's message if err is not nil, else succeeded. A stage still started is ended with it, so
// no run is left with a stage that is open. Call it once, when Query returns.
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
