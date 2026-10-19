package model

import (
	"encoding/json"
	"time"
)

// Statuses of a TrainedModelEvalRun.
const (
	TrainedModelEvalRunRunning   = "running"
	TrainedModelEvalRunCompleted = "completed"
	TrainedModelEvalRunFailed    = "failed"    // Error says why
	TrainedModelEvalRunCancelled = "cancelled" // stopped by the user
)

// Statuses of a TrainedModelCaseResult.
const (
	TrainedModelCaseJudged = "judged" // the model answered and the judge scored it
	TrainedModelCaseFailed = "failed" // the model or the judge failed: Error says why
	// TrainedModelCaseAnswered is never stored: the live stream of an eval run (GET .../evals/{id}/events) uses it for a
	// case the model answered whose batch the judge has not scored yet.
	TrainedModelCaseAnswered = "answered"
)

// Eval schemes of a trained model: how the cases of the eval are made and what the model's answer is compared with.
const (
	// TrainingSchemeFineTuneDataset is the scheme where the eval is the model's own fine-tuning dataset: every example is a
	// case (the question is the training prompt, the expected output is the dataset's output) and the judge scores how
	// closely the model's answer reproduces that output. It measures how well the model learned what it was trained on.
	//
	// The other scheme, the one of the knowledge evals (cases with an input and an output that was never in the training
	// data, checked for), is not implemented for trained models yet; it will be a second value of the same field.
	TrainingSchemeFineTuneDataset = "finetune_dataset"
)

// TrainedModelEvalRun is one run of an eval against a trained model, judged by an external LLM (a "training" row of
// gb_experiment_runs with its gb_training_eval_run_meta; its cases are the case results of the run, see
// TrainedModelCaseResult; evals/training). The HTTP API and the admin UI call it an eval. The eval is a dataset (see
// Scheme), its cases are the dataset's examples: the model is asked the question of each case and the LLM scores its answer
// against the expected output. The counters are updated after every judge batch, so the row shows the progress of a running
// eval run and the result of a finished one.
// Every case is judged or failed (NumJudged + NumFailed = the ones done so far, of NumExamples);
// NumCorrect + NumPartial + NumIncorrect = NumJudged, and ScoreSum is the sum of the scores (0 to 5) of the judged ones.
//
// ID is that of the eval run's gb_experiment_runs row. Status, Params, CreatedAt (the start) and FinishedAt are columns of that
// row, and the rest (Error, JudgeCostUSD, the counters, ...) is in gb_training_eval_run_meta.
type TrainedModelEvalRun struct {
	ID             int64  `json:"id"`
	TrainedModelID int64  `json:"trained_model_id"`
	Status         string `json:"status"`
	// Scheme says how the eval was made: TrainingSchemeFineTuneDataset.
	Scheme string `json:"scheme"`
	// Dataset is the name of the dataset the cases came from (the eval) and DatasetSHA256 the digest of the file that was read.
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
func (e TrainedModelEvalRun) ParamsJSON() string {
	if len(e.Params) == 0 || !json.Valid(e.Params) {
		return "{}"
	}
	return string(e.Params)
}

// SetStoredParams sets Params from the column it is stored in.
func (e *TrainedModelEvalRun) SetStoredParams(s string) {
	e.Params = json.RawMessage("{}")
	if json.Valid([]byte(s)) {
		e.Params = json.RawMessage(s)
	}
}

// TrainedModelCaseResult is the outcome for one example of an eval run: the question, the dataset's answer (Reference), the
// model's (Response) and the judge's score. It is a case result of the eval run's run (a gb_experiment_case_results row with
// status 'completed' or 'failed', whose case_name is Idx) with its gb_training_case_meta row; Status is "judged" or "failed".
type TrainedModelCaseResult struct {
	// ID is that of the case result row and EvalRunID that of the eval run (its run).
	ID        int64 `json:"id"`
	EvalRunID int64 `json:"eval_run_id"`
	// Idx is the example's position in the dataset (0-based), Batch the judge batch it was graded in (1-based).
	Idx         int    `json:"idx"`
	Batch       int    `json:"batch"`
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
