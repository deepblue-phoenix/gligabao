package training

// The HTTP endpoints of the runs of trained models judged by an LLM, the evals and the training data audits: both are a
// TrainedModelRun and they share these endpoints. What a run is and how it runs is core.RunManager (internal/training/runs.go).
//
//   - an EVAL (evals.go) asks the cases of an eval (bin/evals_training/*.json), which the model never saw in training: it
//     measures how well the model generalizes. Its cases have names and it can be part of an experiment;
//   - an AUDIT (audits.go; its logic is internal/training/audits) asks the examples of the model's OWN fine-tuning dataset: it checks
//     that the training took, not how well the model generalizes.

import (
	"database/sql"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

const (
	maxRunBody       = 1 << 14
	runsDefaultLimit = 50
)

// runManager is the endpoints of the runs of one kind. EvalManager and AuditManager are one each.
type runManager struct {
	*core.RunManager
}

// startPrelude is the start of every StartOf: the model's ID, and a sidecar and an LLM to ask and judge with.
func (m *runManager) startPrelude(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return 0, false
	}
	if m.Sc == nil {
		httputil.Error(w, http.StatusServiceUnavailable, "sidecar is not active")
		return 0, false
	}
	if m.LLM == nil {
		httputil.Error(w, http.StatusServiceUnavailable, "no LLM is available to judge the answers (set ANTHROPIC_API_KEY)")
		return 0, false
	}
	return id, true
}

// getModel reads trained model id, answering 404 or 500 itself.
func getModel(w http.ResponseWriter, r *http.Request, db *sql.DB, id int64) (*trainingdb.TrainedModel, bool) {
	tm, err := core.GetModel(r.Context(), db, id)
	if err != nil {
		writeErr(w, err)
		return nil, false
	}
	return tm, true
}

// start starts the run in the background, answering 202 with it: 409 if the model has a run of this kind already, 500 if it
// could not be recorded.
func (m *runManager) start(w http.ResponseWriter, r *http.Request, s core.RunSpec) {
	rec, err := m.Start(r.Context(), s)
	if err != nil {
		writeErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusAccepted, rec)
}

// ListOf serves GET /v1/gb/training/models/{id}/evals|audits[?limit=<n>]: {"evals": [...]} or {"audits": [...]}, the
// runs of trained model {id} (evalsdb.TrainedModelRun, without their cases), newest first, at most limit (default 50, at most
// 500). 404 for an unknown model.
func (m *runManager) ListOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		limit, ok := limitParam(w, r, runsDefaultLimit, trainedModelsMaxLimit)
		if !ok {
			return
		}
		list, err := m.ListRuns(r.Context(), id, limit)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{m.Ops.Plural(): list})
	}
}

// GetOf serves GET /v1/gb/training/evals|audits/{id}: {"run": ..., "cases": [...]}, one run with the cases stored so far
// (evalsdb.TrainedModelCase: question, the expected answer, the model's, score and reason), in the order of the eval or the
// dataset. 404 for an unknown run.
func (m *runManager) GetOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, err := m.GetRun(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		cases, err := m.RunCases(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"run": run, "cases": cases})
	}
}

// CancelOf serves POST /v1/gb/training/evals|audits/{id}/cancel: stops a running run (it ends as "cancelled", keeping
// the batches done so far) and answers 202 with it. 404 for an unknown run, 409 when it is not running.
func (m *runManager) CancelOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		run, err := m.CancelRun(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusAccepted, run)
	}
}
