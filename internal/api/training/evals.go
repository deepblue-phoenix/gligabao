package training

// Evals of trained models: the "Eval" button of the admin UI and the runner internal/evals/training/cmd. An eval run is one run
// (evalsdb.TrainedModelRun, kind "eval") of an eval against a trained model. An eval is a named set of cases
// (*.json files of the evals directory, bin/evals_training by default), each a question and the answer expected; the cases are meant to be ones
// the model never saw in training, so the score says how well it generalizes. The eval is named after the training dataset its
// cases ask about, so a model trained on a dataset has the eval of that name unless another is asked for.
//
// The logic is the eval subsystem internal/evals/training; the manager and the endpoints that follow a run are shared with the training
// data audits (runs.go); this file is what is specific to an eval: its cases, which come from the eval, and its experiments
// (experiments.go).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	trainingeval "github.com/deepblue-phoenix/gligabao/internal/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	core "github.com/deepblue-phoenix/gligabao/internal/training"
	log "github.com/sirupsen/logrus"
)

// EvalManager starts the evals and runs them in the background, one at a time per trained model. Evals still running when the
// server last stopped are marked error when the manager is made. ListOf, GetOf, CancelOf, EventsOf and Wait are those of the
// runManager it is made of.
type EvalManager struct {
	*runManager
	dir string // the evals directory
}

// NewEvalManager makes the manager of the evals of the trained models of dbs, which are the ones in the directory evalDir. sc answers the questions (nil: starting an eval
// answers 503) and llm judges (nil: the same). The evals stop when ctx is done. Evals that a restart interrupted (still
// "running" in the database) are marked error.
func NewEvalManager(ctx context.Context, dbs []core.Database, evalDir string, sc core.Inferer, llm memory_core.LLM) *EvalManager {
	return &EvalManager{&runManager{core.NewRunManager(ctx, dbs, sc, llm, core.EvalOps)}, evalDir}
}

// StartOf serves POST /v1/gb/training/models/{id}/evals[?db=<name>] with an optional JSON body {"eval": "", "cases": [], "limit": 0,
// "batch_size": 10, "judge_model": "", "max_tokens": 1024, "temperature": 0.1}: it starts an eval of trained model {id} and answers
// 202 with the eval run (evalsdb.TrainedModelRun, status "running"); the work goes on in the background (see GetOf, ListOf, CancelOf).
//   - eval: the name of the eval to run (see DefinitionsOf); default: the eval named like the model's training dataset
//   - cases: run only the cases with these names (default: all of the eval's)
//   - limit: ask only the first n cases (default: all of them)
//   - batch_size: question/answer pairs per judge call (default 10, at most 25)
//   - judge_model: the LLM model that judges (default: the server's default model)
//   - max_tokens, temperature: of the model's answers (default 1024 and 0.1)
//   - experiment: make the eval a run of the training experiment of that name (created if needed, see experiments.go);
//     experiment_description and experiment_config (an object) are stored when it is created
//
// 404 for an unknown model, 400 for a bad body, an unknown eval or case, or no eval to default to (the model has no dataset, or no
// eval has its name), 409 when an eval of this model is running already, 503 without a sidecar or an LLM.
func (m *EvalManager) StartOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, id, ok := m.startPrelude(w, r)
		if !ok {
			return
		}
		var in struct {
			core.RunOptions
			Eval  string   `json:"eval"`
			Cases []string `json:"cases"`

			Experiment            string         `json:"experiment"`
			ExperimentDescription string         `json:"experiment_description"`
			ExperimentConfig      map[string]any `json:"experiment_config"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			httputil.Error(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		cfg, params, err := in.Resolve()
		switch {
		case err != nil:
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		case len(strings.TrimSpace(in.Experiment)) > core.MaxExperimentName:
			httputil.Error(w, http.StatusBadRequest, "experiment must be at most "+strconv.Itoa(core.MaxExperimentName)+" characters")
			return
		}
		tm, ok := getModel(w, r, n, id)
		if !ok {
			return
		}
		name := strings.TrimSpace(in.Eval)
		if name == "" {
			if name = strings.TrimSuffix(tm.Dataset, ".jsonl"); name == "" || !core.HasEval(m.dir, name) {
				httputil.Error(w, http.StatusBadRequest, "there is no eval named like this model's training dataset"+
					"; set \"eval\" to one of: "+strings.Join(trainingeval.EvalNames(m.dir), ", "))
				return
			}
		}
		cases, err := trainingeval.LoadEvals(m.dir, in.Cases, name)
		if err != nil {
			httputil.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if params.Limit > 0 && params.Limit < len(cases) {
			cases = cases[:params.Limit]
		}
		spec := core.RunSpec{Database: n, Model: tm, Config: cfg, Params: params, Row: evalsdb.TrainedModelRun{Eval: name},
			Examples: make([]trainingeval.Example, len(cases)), Names: make([]string, len(cases))}
		for i, c := range cases {
			spec.Examples[i], spec.Names[i] = c.Example(), c.Name
		}
		if len(in.Cases) > 0 {
			spec.Params.Cases = spec.Names
		}
		if x := strings.TrimSpace(in.Experiment); x != "" {
			spec.AfterRecord = func(ctx context.Context, runID int64) error {
				err := core.JoinExperiment(ctx, n.DB, x, in.ExperimentDescription, in.ExperimentConfig, runID)
				if err != nil {
					log.WithError(err).WithField("experiment", x).Error("training: adding the eval to its experiment failed")
				}
				return err
			}
		}
		m.start(w, r, spec)
	}
}

// DefinitionsOf serves GET /v1/gb/training/eval-definitions: {"evals": [{"name": ..., "cases": [{"name", "instruction", "input",
// "output"}, ...]}, ...]}, the evals the server can run (the ones in the directory evalDir), by name. The "name" of an eval is what the "eval"
// of POST .../evals takes.
func DefinitionsOf(evalDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		evals, err := trainingeval.Evals(evalDir)
		if err != nil {
			log.WithError(err).Error("training: load the eval definitions failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to load the evals")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"evals": evals})
	}
}
