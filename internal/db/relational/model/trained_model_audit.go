package model

import (
	"encoding/json"
	"time"
)

//---------------------------------------------------------------------------------------------
// Training data audits of trained models. An AUDIT asks a trained model the questions of its OWN fine-tuning dataset and has an
// LLM judge how closely its answers reproduce the dataset's: it checks that the training took (learning rate, masking and format
// bugs, capacity), and, through the examples it fails, finds bad training data (label noise, contradictions). Every case is
// something the model was trained on, so the score says how well it memorized and fitted its data, NOT how well it generalizes.
//
// It is separate from an EVAL of a trained model (TrainedModelEvalRun), whose cases are meant to be ones the model never saw. An
// audit has its own tables (gb_training_audit_runs and gb_training_audit_cases, see sqlite/db_schema.go) and is in no experiment.

// TrainedModelAuditRun is one audit of a trained model against its own fine-tuning dataset (a gb_training_audit_runs row; its
// cases are TrainedModelAuditCase rows). Each example of the dataset is a case: the model is asked its question and the LLM
// scores the answer against the dataset's output. The counters are updated after every judge batch, so the row shows the
// progress of a running audit and the result of a finished one.
// Every case is judged or failed (NumJudged + NumFailed = the ones done so far, of NumExamples);
// NumCorrect + NumPartial + NumIncorrect = NumJudged, and ScoreSum is the sum of the scores (0 to 5) of the judged ones.
type TrainedModelAuditRun struct {
	ID             int64  `json:"id"`
	TrainedModelID int64  `json:"trained_model_id"`
	Status         string `json:"status"`
	// Dataset is the name of the training dataset the cases came from and DatasetSHA256 the digest of the file that was read.
	Dataset       string `json:"dataset"`
	DatasetSHA256 string `json:"dataset_sha256"`
	// Params are the settings of the run as a JSON object (batch_size, max_tokens, temperature, limit).
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
func (a TrainedModelAuditRun) ParamsJSON() string {
	if len(a.Params) == 0 || !json.Valid(a.Params) {
		return "{}"
	}
	return string(a.Params)
}

// SetStoredParams sets Params from the column it is stored in.
func (a *TrainedModelAuditRun) SetStoredParams(s string) {
	a.Params = json.RawMessage("{}")
	if json.Valid([]byte(s)) {
		a.Params = json.RawMessage(s)
	}
}

// TrainedModelAuditCase is the outcome for one example of an audit run: the question, the dataset's answer (Reference), the
// model's (Response) and the judge's score. Status is "judged" or "failed".
type TrainedModelAuditCase struct {
	ID         int64 `json:"id"`
	AuditRunID int64 `json:"audit_run_id"`
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
