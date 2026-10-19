package model

import (
	"encoding/json"
	"time"
)

//---------------------------------------------------------------------------------------------
// Training experiments: the experiments of the eval runs of trained models, the rows of type "training" of gb_experiments and
// gb_experiment_runs (see sqlite/db_schema.go). The knowledge-extraction eval experiments (Experiment, ExperimentRun) are the
// rows of type "knowledge" of the same tables.

// TrainingExperiment is a named series of runs, each of which is an eval run of a trained model (see TrainingExperimentRun).
// Names are unique.
type TrainingExperiment struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Config      map[string]any `json:"config"` // free-form settings under test (the training recipe, the judge, ...)
	CreatedAt   time.Time      `json:"created_at"`
	// NumRuns is how many runs the experiment has (filled by the getters and the lists).
	NumRuns int `json:"num_runs"`
}

// TrainingExperimentRun is one run of a training experiment: an eval run (TrainedModelEvalRun) of one trained model. The run
// holds the link; EvalRun is the eval run as it is now (status, counters, score and the judge's cost, so a running run shows its
// progress) and Model the model that was evaluated.
type TrainingExperimentRun struct {
	ID           int64 `json:"id"` // the same as EvalRun.ID: the eval run is the run
	ExperimentID int64 `json:"experiment_id"`
	// Status is the eval run's status (TrainedModelEvalRunRunning, ...Passed, ...Failed or ...Cancelled).
	Status    string                  `json:"status"`
	CreatedAt time.Time               `json:"created_at"`
	EvalRun   TrainedModelEvalRun     `json:"eval"`
	Model     TrainingExperimentModel `json:"model"`
	// MeanScore is the mean judge score (0 to 5) of the judged answers and CorrectRate the share of them that is correct
	// (a score of 4 or 5); both are nil while nothing is judged.
	MeanScore   *float64 `json:"mean_score"`
	CorrectRate *float64 `json:"correct_rate"`
}

// Summarize sets Status, MeanScore and CorrectRate from EvalRun.
func (r *TrainingExperimentRun) Summarize() {
	r.Status = r.EvalRun.Status
	r.MeanScore, r.CorrectRate = nil, nil
	if n := r.EvalRun.NumJudged; n > 0 {
		mean := float64(r.EvalRun.ScoreSum) / float64(n)
		rate := float64(r.EvalRun.NumCorrect) / float64(n)
		r.MeanScore, r.CorrectRate = &mean, &rate
	}
}

// TrainingExperimentModel is what a run says about the trained model that was evaluated: enough to compare runs by
// training recipe (the full record is TrainedModel).
type TrainingExperimentModel struct {
	ID            int64           `json:"id"`
	Name          string          `json:"name"`
	BaseModel     string          `json:"base_model"`
	ModelSize     string          `json:"model_size"`
	NumTrainSteps int             `json:"num_train_steps"`
	FinalLoss     *float64        `json:"final_loss"`
	Params        json.RawMessage `json:"params"` // the other training settings (LoRA rank, learning rate, ...)
}

// SetStoredParams sets Params from the column it is stored in.
func (m *TrainingExperimentModel) SetStoredParams(s string) {
	m.Params = json.RawMessage("{}")
	if json.Valid([]byte(s)) {
		m.Params = json.RawMessage(s)
	}
}
