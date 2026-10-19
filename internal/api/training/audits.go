package training

// Training data audits of trained models: the "Audit" button of the admin UI. An audit is one run (evalsdb.TrainedModelRun, kind
// "audit") of a trained model against its OWN fine-tuning dataset: it asks the model the question of every example (a case) and
// has the external LLM judge how closely the answer reproduces the dataset's output. Every case is something the model was trained
// on, so the score says whether the training took (and, through the examples it fails, which training data is bad); it is NOT an
// eval: it does not measure how well the model generalizes. The evals of trained models (evals.go) are the separate thing, for
// cases the model never saw. The two share the manager and the endpoints that follow a run (runs.go); this file is what is
// specific to an audit: where its cases come from.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
)

// AuditManager starts the training data audits and runs them in the background, one at a time per trained model. Audits still
// running when the server last stopped are marked error when the manager is made. ListOf, GetOf, CancelOf, EventsOf and Wait are
// those of the runManager it is made of.
type AuditManager struct {
	*runManager
	dir string // the training datasets directory
}

// NewAuditManager makes the manager of the training data audits of the trained models in db, whose datasets are in the training datasets directory datasetDir. sc answers the questions (nil:
// starting an audit answers 503) and llm judges (nil: the same). The audits stop when ctx is done. Audits that a restart
// interrupted (still "running" in the database) are marked error.
func NewAuditManager(ctx context.Context, db *sql.DB, datasetDir string, sc core.Inferer, llm memory_core.LLM) *AuditManager {
	return &AuditManager{&runManager{core.NewRunManager(ctx, db, sc, llm, core.AuditOps)}, datasetDir}
}

// StartOf serves POST /v1/gb/training/models/{id}/audits with an optional JSON body {"limit": 0, "batch_size": 10,
// "judge_model": "", "max_tokens": 1024, "temperature": 0.1}: it starts a training data audit of trained model {id} and answers 202
// with the audit (evalsdb.TrainedModelRun, status "running"); the work goes on in the background (see GetOf, ListOf, CancelOf).
//   - limit: ask only the first n cases (default: all of them)
//   - batch_size: question/answer pairs per judge call (default 10, at most 25)
//   - judge_model: the LLM model that judges (default: the server's default model)
//   - max_tokens, temperature: of the model's answers (default 1024 and 0.1)
//
// 404 for an unknown model, 400 for a bad body or when the model's dataset cannot be read (none recorded, gone from the training
// datasets directory, invalid), 409 when an audit of this model is running already, 503 without a sidecar or an LLM.
func (m *AuditManager) StartOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := m.startPrelude(w, r)
		if !ok {
			return
		}
		var in core.RunOptions
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		cfg, params, err := in.Resolve()
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		tm, ok := getModel(w, r, m.DB, id)
		if !ok {
			return
		}
		dataset, sum, examples, err := core.LoadModelDataset(m.dir, tm)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if params.Limit > 0 && params.Limit < len(examples) {
			examples = examples[:params.Limit]
		}
		m.start(w, r, core.RunSpec{Model: tm, Config: cfg, Params: params, Examples: examples,
			Row: evalsdb.TrainedModelRun{Dataset: dataset, DatasetSHA256: sum}})
	}
}
