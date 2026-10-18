package model

import (
	"encoding/json"
	"time"
)

//---------------------------------------------------------------------------------------------
// Training experiments: the experiments of the evals of trained models (gb_training_experiments and
// gb_training_experiment_runs, see sqlite/db_schema.go). They are separate from the knowledge-extraction eval
// experiments (Experiment, ExperimentRun).

// TrainingExperiment is a named series of runs, each of which is an eval of a trained model (see TrainingExperimentRun).
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

// TrainingExperimentRun is one run of a training experiment: an eval (gb_trained_model_evals) of one trained model. The run
// holds the link; Eval is the eval as it is now (status, counters, score and the judge's cost, so a running run shows its
// progress) and Model the model that was evaluated.
type TrainingExperimentRun struct {
	ID           int64 `json:"id"`
	ExperimentID int64 `json:"experiment_id"`
	EvalID       int64 `json:"eval_id"`
	// Status is the eval's status (TrainedModelEvalRunning, ...Completed, ...Failed or ...Cancelled).
	Status    string                  `json:"status"`
	CreatedAt time.Time               `json:"created_at"`
	Eval      TrainedModelEval        `json:"eval"`
	Model     TrainingExperimentModel `json:"model"`
	// MeanScore is the mean judge score (0 to 5) of the judged answers and CorrectRate the share of them that is correct
	// (a score of 4 or 5); both are nil while nothing is judged.
	MeanScore   *float64 `json:"mean_score"`
	CorrectRate *float64 `json:"correct_rate"`
}

// Summarize sets Status, MeanScore and CorrectRate from Eval.
func (r *TrainingExperimentRun) Summarize() {
	r.Status = r.Eval.Status
	r.MeanScore, r.CorrectRate = nil, nil
	if n := r.Eval.NumJudged; n > 0 {
		mean := float64(r.Eval.ScoreSum) / float64(n)
		rate := float64(r.Eval.NumCorrect) / float64(n)
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
