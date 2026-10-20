package model

import (
	"encoding/json"
	"time"
)

//---------------------------------------------------------------------------------------------
// Runs of trained models judged by an LLM: the evals. An EVAL run is one run of an eval, a named set of cases, against a trained
// model. The cases are meant to be ones the model never saw, so the score says how well it generalizes. It is a "training" run of
// the universal eval tables (gb_experiment_runs, gb_experiment_case_runs, with gb_training_eval_run_meta and gb_training_case_meta),
// so it can be part of an experiment; its cases have names (the eval's case names). The types, the statuses
// (trained_model_status.go) and the engine (internal/evals/training) are those of the HTTP shapes and the admin UI's live view.
//
// The training data audits (asking the model the questions of its own training dataset) are not here: internal/training_audits has
// them, with their own types and tables.

// The kinds of run of a trained model (TrainedModelRun.Kind).
const (
	TrainedModelKindEval = "eval"
)

// TrainedModelRun is one run of a trained model against a set of cases, judged by an external LLM: the model is asked the question
// of each case and the LLM scores its answer against the expected output. The counters
// are updated after every judge batch, so the row shows the progress of a running run and the result of a finished one.
// Every case is judged or errored (NumJudged + NumErrored = the ones done so far, of NumExamples);
// NumCorrect + NumPartial + NumIncorrect = NumJudged, and ScoreSum is the sum of the scores (0 to 5) of the judged ones.
//
// For an eval, ID is that of the run's gb_experiment_runs row; Status, Params, CreatedAt (the start) and FinishedAt are columns of
// that row, and the rest (Error, JudgeCostUSD, the counters, ...) is in gb_training_eval_run_meta.
type TrainedModelRun struct {
	ID             int64 `json:"id"`
	TrainedModelID int64 `json:"trained_model_id"`
	// Kind is TrainedModelKindEval. It is not stored.
	Kind   string `json:"kind"`
	Status string `json:"status"`
	// Eval is the name of the eval whose cases were asked: a file name of bin/evals_training
	// without ".json", the same as the eval column of the run's case results.
	Eval string `json:"eval,omitempty"`
	// Params are the settings of the run as a JSON object (batch_size, max_tokens, temperature, limit).
	Params json.RawMessage `json:"params"`
	// JudgeModel is the model that judged ("" until its first reply).
	JudgeModel   string `json:"judge_model"`
	NumExamples  int    `json:"num_examples"`
	NumAnswered  int    `json:"num_answered"`
	NumJudged    int    `json:"num_judged"`
	NumErrored   int    `json:"num_errored"`
	NumCorrect   int    `json:"num_correct"`
	NumPartial   int    `json:"num_partial"`
	NumIncorrect int    `json:"num_incorrect"`
	ScoreSum     int    `json:"score_sum"`
	// NumBatches is how many judge batches are done; JudgeCalls the LLM calls the judge made (a batch can need a retry).
	NumBatches        int        `json:"num_batches"`
	JudgeCalls        int        `json:"judge_calls"`
	JudgeInputTokens  int        `json:"judge_input_tokens"`
	JudgeOutputTokens int        `json:"judge_output_tokens"`
	JudgeCostUSD      float64    `json:"judge_cost_usd"`
	Error             string     `json:"error"`
	CreatedAt         time.Time  `json:"created_at"`
	FinishedAt        *time.Time `json:"finished_at"`
}

// ParamsJSON is Params as stored ("{}" when there are none or they are not valid JSON).
func (r TrainedModelRun) ParamsJSON() string {
	if len(r.Params) == 0 || !json.Valid(r.Params) {
		return "{}"
	}
	return string(r.Params)
}

// SetStoredParams sets Params from the column it is stored in.
func (r *TrainedModelRun) SetStoredParams(s string) {
	r.Params = json.RawMessage("{}")
	if json.Valid([]byte(s)) {
		r.Params = json.RawMessage(s)
	}
}

// TrainedModelCase is the outcome for one case of a run: the question, the expected answer (Reference), the model's (Response)
// and the judge's score. Status is TrainedModelCaseJudged or TrainedModelCaseErrored. For an eval it is a
// gb_experiment_case_runs row (whose case_name is Name) with its gb_training_case_meta row.
type TrainedModelCase struct {
	// ID is that of the stored row and RunID that of the run.
	ID    int64 `json:"id"`
	RunID int64 `json:"run_id"`
	// Idx is the case's position in the eval or the dataset (0-based), Batch the judge batch it was graded in (1-based).
	Idx   int `json:"idx"`
	Batch int `json:"batch"`
	// Name is the name of the case within its eval (an eval run's case only; "" for an audit's, which are dataset examples).
	Name        string `json:"name,omitempty"`
	Instruction string `json:"instruction"`
	Input       string `json:"input"`
	Reference   string `json:"reference"`
	Response    string `json:"response"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	// Score is the judge's, 0 to 5 (nil unless the case is judged); Reason its explanation.
	Score  *int   `json:"score"`
	Reason string `json:"reason"`
	// InputTokens, OutputTokens and DurationMS are the model's for this question.
	InputTokens  int   `json:"input_tokens"`
	OutputTokens int   `json:"output_tokens"`
	DurationMS   int64 `json:"duration_ms"`
}
