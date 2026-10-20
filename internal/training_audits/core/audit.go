package core

import "time"

// AuditRun is one execution of an audit: the model of TrainedModelID asked the examples of Dataset (one AuditCaseRun each, which
// point back here through AuditCaseRun.RunID), and an LLM judging the answers in batches. It holds no list of its case runs.
type AuditRun struct {
	ID AuditRunID `json:"id"`
	// TrainedModelID is the trained model that was asked. It is not a foreign key: the trained models are not part of this schema.
	TrainedModelID int64 `json:"trained_model_id"`
	// Dataset is the name of the training dataset whose examples were asked (without ".jsonl") and DatasetSHA256 the digest of the
	// file that was read.
	Dataset       string `json:"dataset"`
	DatasetSHA256 string `json:"dataset_sha256,omitempty"`

	Status Status `json:"status"`
	Error  string `json:"error,omitempty"` // why it errored (StatusErrored only)
	// Params are how this run was invoked (batch size, max tokens, temperature, limit, ...).
	Params Params `json:"params,omitempty"`
	// Counts are over its case runs; Counts.Total is the number of examples asked (the dataset's, or the first Limit of them).
	Counts Counts `json:"counts"`
	// CostUSD is the LLM cost of the judge (the questions asked to the trained model cost nothing by themselves).
	CostUSD float64 `json:"cost_usd"`
	AuditStats
	Timing

	// CaseRuns are its case runs, in dataset order. Filled by getters only; not stored with the run.
	CaseRuns []AuditCaseRun `json:"case_runs,omitempty"`
}

// AuditStats are the statistics of an AuditRun as a whole. The counters are rewritten after every judge batch, so a running audit
// shows its progress.
//
// The questions asked are the Counts.Total of the run and the ones where the model or the judge errored its Counts.Errored. Every
// question that is not errored is Answered; the Judged ones are scored: NumCorrect + NumPartial + NumIncorrect = NumJudged, split by
// the judge's score (>= 4, 2 to 3, 0 to 1).
type AuditStats struct {
	// JudgeModel is the model that judged, as the LLM reported it ("" until its first reply).
	JudgeModel   string `json:"judge_model,omitempty"`
	NumAnswered  int    `json:"num_answered"`
	NumJudged    int    `json:"num_judged"`
	NumCorrect   int    `json:"num_correct"`
	NumPartial   int    `json:"num_partial"`
	NumIncorrect int    `json:"num_incorrect"`
	// ScoreSum is the sum of the judge's scores (0 to 5 each) of the judged ones: the mean is ScoreSum / NumJudged.
	ScoreSum int `json:"score_sum"`
	// NumBatches is the number of judge batches done, JudgeCalls the LLM calls the judge made (a batch may need a retry).
	NumBatches        int `json:"num_batches"`
	JudgeCalls        int `json:"judge_calls"`
	JudgeInputTokens  int `json:"judge_input_tokens"`
	JudgeOutputTokens int `json:"judge_output_tokens"`
}

// AuditCaseRun is one example of the dataset of an AuditRun, asked to the trained model and judged: the question and the answer it
// was trained to give (as they were when the case ran), what the model answered, and the judge's score.
type AuditCaseRun struct {
	ID    AuditCaseRunID `json:"id"`
	RunID AuditRunID     `json:"run_id"`
	// Idx is the example's position in the dataset's examples that the run was given (0-based).
	Idx int `json:"idx"`

	// Status is the lifecycle: did it run to the end. Error says why not (StatusErrored only).
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`

	// Instruction, Input and Reference are what was asked and the answer the model was trained to give.
	Instruction string `json:"instruction"`
	Input       string `json:"input,omitempty"`
	Reference   string `json:"reference,omitempty"`
	// Response is the answer of the trained model ("" if it failed).
	Response string `json:"response,omitempty"`
	// Batch is the judge batch that scored the case, from 1 (0 if it was not judged).
	Batch int `json:"batch"`
	// JudgeScore is the score of the judge, 0 to 5 (nil unless the case completed); JudgeReason is its explanation.
	JudgeScore  *int   `json:"judge_score,omitempty"`
	JudgeReason string `json:"judge_reason,omitempty"`
	// InputTokens, OutputTokens and DurationMS are those of the answer of the model.
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	DurationMS   int       `json:"duration_ms"`
	CreatedAt    time.Time `json:"created_at"`
}

// Score is the JudgeScore in [0, 1] (nil if the case is not judged).
func (c AuditCaseRun) Score() *float64 {
	if c.JudgeScore == nil {
		return nil
	}
	s := float64(*c.JudgeScore) / 5
	return &s
}
