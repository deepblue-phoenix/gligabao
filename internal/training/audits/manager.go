package audits

import (
	"context"
	"database/sql"

	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/training"
)

// Manager starts the training data audits and runs them in the background, one at a time per trained model. It is a
// training.RunManager (which also lists, reads, cancels and watches the audits) that knows where an audit's cases come from: the
// training dataset of the model. Audits still running when the server last stopped are marked error when the manager is made.
type Manager struct {
	*training.RunManager
	dir string // the training datasets directory
}

// NewManager makes the manager of the audits of the trained models in db, whose datasets are in the training datasets directory
// datasetDir. sc answers the questions and llm judges (the callers check for nil). The audits stop when ctx is done.
func NewManager(ctx context.Context, db *sql.DB, datasetDir string, sc training.Inferer, llm memory_core.LLM) *Manager {
	return &Manager{training.NewRunManager(ctx, db, sc, llm, Ops), datasetDir}
}

// Start records an audit of trained model modelID and starts it in the background, returning the recorded audit (status
// "running"). opts.Limit keeps only the first examples of the model's dataset. Errors are *training.Error: KindInvalid for bad
// options or when the model's dataset cannot be read (none recorded, gone from the training datasets directory, invalid),
// KindNotFound for an unknown model, KindConflict if the model has an audit running already, KindInternal.
func (m *Manager) Start(ctx context.Context, modelID int64, opts training.RunOptions) (*evalsdb.TrainedModelRun, error) {
	cfg, params, err := opts.Resolve()
	if err != nil {
		return nil, &training.Error{Kind: training.KindInvalid, Msg: err.Error()}
	}
	tm, err := training.GetModel(ctx, m.DB, modelID)
	if err != nil {
		return nil, err
	}
	dataset, sum, examples, err := LoadDataset(m.dir, tm)
	if err != nil {
		return nil, &training.Error{Kind: training.KindInvalid, Msg: err.Error()}
	}
	if params.Limit > 0 && params.Limit < len(examples) {
		examples = examples[:params.Limit]
	}
	return m.RunManager.Start(ctx, training.RunSpec{Model: tm, Config: cfg, Params: params, Examples: examples,
		Row: evalsdb.TrainedModelRun{Dataset: dataset, DatasetSHA256: sum}})
}
