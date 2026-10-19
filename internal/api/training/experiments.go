package training

// Training experiments: named series of evals of trained models, to compare trained models (training recipes) by their eval
// scores. An experiment (evalsdb.TrainingExperiment) is made by starting an eval with an "experiment" name (see StartOf); the
// eval is then a run of it (evalsdb.TrainingExperimentRun). They are separate from the knowledge-extraction experiments of
// internal/evals/knowledge.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/deepblue-phoenix/gligabao/internal/api/httputil"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	log "github.com/sirupsen/logrus"
)

// maxExperimentName is the longest name of a training experiment.
const maxExperimentName = 120

// joinExperiment makes the eval a run of the experiment called name, creating the experiment first if needed.
func joinExperiment(ctx context.Context, d *sql.DB, name, description string, cfg map[string]any, evalID int64) error {
	x, err := evalsdb.EnsureTrainingExperiment(ctx, d, name, description, cfg)
	if err != nil {
		return err
	}
	_, err = evalsdb.StartTrainingExperimentRun(ctx, d, x.ID, evalID)
	return err
}

// ExperimentsOf serves GET /v1/gb/training/experiments[?db=<name>]: {"experiments": [...]}, the training experiments
// (evalsdb.TrainingExperiment, with their number of runs), newest first.
func (m *EvalManager) ExperimentsOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(m.dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		list, err := evalsdb.ListTrainingExperiments(r.Context(), n.DB)
		if err != nil {
			log.WithError(err).Error("training: list experiments failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the experiments")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"experiments": list})
	}
}

// ExperimentOf serves GET /v1/gb/training/experiments/{id}[?db=<name>]: {"experiment": ..., "runs": [...]}, one experiment with
// its runs, oldest first. A run (evalsdb.TrainingExperimentRun) carries its eval as it is now (so a running one shows its
// progress), the trained model that was evaluated, and the mean score and the share of correct answers. 404 for an unknown experiment.
func (m *EvalManager) ExperimentOf() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, ok := pick(m.dbs, r.URL.Query().Get("db"))
		if !ok {
			httputil.Error(w, http.StatusNotFound, "unknown database")
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		x, err := evalsdb.GetTrainingExperiment(r.Context(), n.DB, id)
		if errors.Is(err, relational.ErrNotFound) {
			httputil.Error(w, http.StatusNotFound, "unknown experiment")
			return
		} else if err != nil {
			log.WithError(err).Error("training: read experiment failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to read the experiment")
			return
		}
		runs, err := evalsdb.ListTrainingExperimentRuns(r.Context(), n.DB, id)
		if err != nil {
			log.WithError(err).Error("training: list experiment runs failed")
			httputil.Error(w, http.StatusInternalServerError, "failed to list the experiment's runs")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"experiment": x, "runs": runs})
	}
}
