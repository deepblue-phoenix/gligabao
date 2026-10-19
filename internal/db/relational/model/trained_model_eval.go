package model

import (
	"encoding/json"
	"time"
)

// Statuses of a TrainedModelEval.
const (
	TrainedModelEvalRunning   = "running"
	TrainedModelEvalCompleted = "completed"
	TrainedModelEvalFailed    = "failed"    // Error says why
	TrainedModelEvalCancelled = "cancelled" // stopped by the user
)

// Statuses of a TrainedModelEvalItem.
const (
	TrainedModelEvalItemJudged = "judged" // the model answered and the judge scored it
	TrainedModelEvalItemFailed = "failed" // the model or the judge failed: Error says why
	// TrainedModelEvalItemAnswered is never stored: the live stream of an eval (GET .../evals/{id}/events) uses it for an
	// item the model answered whose batch the judge has not scored yet.
	TrainedModelEvalItemAnswered = "answered"
)

// TrainedModelEval is one run of the LLM-as-a-judge eval of a trained model (a "training" row of gb_experiment_results with its gb_training_eval_meta; evals/training): the
// model is asked the questions of a dataset and an external LLM scores its answers against the dataset's. The counters are
// updated after every judge batch, so the row shows the progress of a running eval and the result of a finished one.
// Every example is judged or failed (NumJudged + NumFailed = the ones done so far, of NumExamples);
// NumCorrect + NumPartial + NumIncorrect = NumJudged, and ScoreSum is the sum of the scores (0 to 5) of the judged ones.
//
// ID is that of the eval's gb_experiment_results row. Status, Error, CreatedAt (the start), FinishedAt and JudgeCostUSD are
// columns of that row, Params those of its run (gb_experiment_runs), and the rest is in gb_training_eval_meta.
type TrainedModelEval struct {
	ID             int64  `json:"id"`
	TrainedModelID int64  `json:"trained_model_id"`
	Status         string `json:"status"`
	// Dataset is the name of the dataset the questions came from and DatasetSHA256 the digest of the file that was read.
	Dataset       string `json:"dataset"`
	DatasetSHA256 string `json:"dataset_sha256"`
	// Params are the settings of the run as a JSON object (batch_size, concurrency, max_tokens, temperature, limit).
	Params json.RawMessage `json:"params"`
	// JudgeModel is the model that judged ("" until its first reply).
	JudgeModel   string `json:"judge_model"`
	NumExamples  int    `json:"num_examples"`
	NumAnswered  int    `json:"num_answered"`
	NumJudged    int    `json:"num_judged"`
	NumFailed    int    `json:"num_failed"`
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
func (e TrainedModelEval) ParamsJSON() string {
	if len(e.Params) == 0 || !json.Valid(e.Params) {
		return "{}"
	}
	return string(e.Params)
}

// SetStoredParams sets Params from the column it is stored in.
func (e *TrainedModelEval) SetStoredParams(s string) {
	e.Params = json.RawMessage("{}")
	if json.Valid([]byte(s)) {
		e.Params = json.RawMessage(s)
	}
}

// TrainedModelEvalItem is the outcome for one example of an eval (gb_training_eval_items): the question, the
// dataset's answer (Reference), the model's (Response) and the judge's score.
type TrainedModelEvalItem struct {
	ID     int64 `json:"id"`
	EvalID int64 `json:"eval_id"`
	// Idx is the example's position in the dataset (0-based), Batch the judge batch it was graded in (1-based).
	Idx         int    `json:"idx"`
	Batch       int    `json:"batch"`
	Instruction string `json:"instruction"`
	Input       string `json:"input"`
	Reference   string `json:"reference"`
	Response    string `json:"response"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	// Score is the judge's, 0 to 5 (nil unless the item is judged); Reason its explanation.
	Score  *int   `json:"score"`
	Reason string `json:"reason"`
	// InputTokens, OutputTokens and DurationMS are the model's for this question.
	InputTokens  int   `json:"input_tokens"`
	OutputTokens int   `json:"output_tokens"`
	DurationMS   int64 `json:"duration_ms"`
}
