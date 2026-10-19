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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	trainingeval "github.com/deepblue-phoenix/gligabao/internal/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	log "github.com/sirupsen/logrus"
)

// AuditManager starts the training data audits and runs them in the background, one at a time per trained model. Audits still
// running when the server last stopped are marked error when the manager is made. ListOf, GetOf, CancelOf, EventsOf and Wait are
// those of the runManager it is made of.
type AuditManager struct{ *runManager }

// NewAuditManager makes the manager of the training data audits of the trained models of dbs. sc answers the questions (nil:
// starting an audit answers 503) and llm judges (nil: the same). The audits stop when ctx is done. Audits that a restart
// interrupted (still "running" in the database) are marked error.
func NewAuditManager(ctx context.Context, dbs []Database, sc Inferer, llm memory_core.LLM) *AuditManager {
	return &AuditManager{newRunManager(ctx, dbs, sc, llm, auditOps)}
}

// StartOf serves POST /v1/gb/training/models/{id}/audits[?db=<name>] with an optional JSON body {"limit": 0, "batch_size": 10,
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
		n, id, ok := m.startPrelude(w, r)
		if !ok {
			return
		}
		var in RunOptions
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		cfg, params, err := in.resolve()
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		tm, ok := getModel(w, r, n, id)
		if !ok {
			return
		}
		dataset, sum, examples, err := loadModelDataset(n, tm)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if params.Limit > 0 && params.Limit < len(examples) {
			examples = examples[:params.Limit]
		}
		m.start(w, r, runSpec{n: n, tm: tm, cfg: cfg, params: params, examples: examples,
			row: evalsdb.TrainedModelRun{Dataset: dataset, DatasetSHA256: sum}})
	}
}

// loadModelDataset reads the training dataset of tm from n's training datasets directory: the dataset's name, the digest of
// the file and its examples. The errors are safe to show to the user.
func loadModelDataset(n Database, tm *trainingdb.TrainedModel) (name, sum string, examples []trainingeval.Example, err error) {
	name = strings.TrimSuffix(tm.Dataset, ".jsonl")
	if name == "" {
		return "", "", nil, errors.New("this model was trained on the sidecar's default dataset, which gligabao does not have: there are no questions to ask")
	}
	ds, err := findDataset(n, name)
	if err != nil {
		return "", "", nil, fmt.Errorf("the training dataset %s of this model is not in the training datasets directory", name)
	}
	content, err := os.ReadFile(ds.Path)
	if err != nil {
		log.WithError(err).WithField("dataset", name).Error("training: read training dataset file failed")
		return "", "", nil, fmt.Errorf("the data file of dataset %s cannot be read", name)
	}
	if len(content) > MaxDatasetBytes {
		return "", "", nil, fmt.Errorf("the data file of dataset %s is over %d MB", name, MaxDatasetBytes>>20)
	}
	if examples, err = trainingeval.ParseExamples(content); err != nil {
		return "", "", nil, fmt.Errorf("dataset %s: %w", name, err)
	}
	h := sha256.Sum256(content)
	return name, hex.EncodeToString(h[:]), examples, nil
}
