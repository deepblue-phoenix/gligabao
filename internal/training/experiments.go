package training

import (
	"context"
	"database/sql"

	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
)

// MaxExperimentName is the longest name of a training experiment.
const MaxExperimentName = 120

// JoinExperiment makes the eval a run of the experiment called name, creating the experiment first if needed.
func JoinExperiment(ctx context.Context, d *sql.DB, name, description string, cfg map[string]any, evalID int64) error {
	x, err := evalsdb.EnsureTrainingExperiment(ctx, d, name, description, cfg)
	if err != nil {
		return err
	}
	_, err = evalsdb.StartTrainingExperimentRun(ctx, d, x.ID, evalID)
	return err
}
