package core

// KnowledgeEvalRunMeta is the metadata of a knowledge EvalRun (table gb_neo_eval_runs_knowledge_meta): the statistics of the
// whole run, summed over its case runs. The counters are rewritten as the case runs finish, so a running eval run shows its
// progress. The pass and fail of the cases are not here: they are the Counts of the EvalRun.
type KnowledgeEvalRunMeta struct {
	EvalRunID EvalRunID `json:"eval_run_id"`
	// Chunks is the total number of chunks the remember calls reported (0 if no text was chunked).
	Chunks int `json:"chunks"`
	// Steps is the total of ATIF steps over the trajectories of all the cases (the sum of KnowledgeCaseRunMeta.Steps).
	Steps int `json:"steps"`
	// RememberCalls and RecallCalls are the calls made, and RememberCostUSD and RecallCostUSD what they cost. The two costs add
	// up to the EvalRun's CostUSD.
	RememberCalls   int     `json:"remember_calls"`
	RecallCalls     int     `json:"recall_calls"`
	RememberCostUSD float64 `json:"remember_cost_usd"`
	RecallCostUSD   float64 `json:"recall_cost_usd"`
	// ChecksPassed and ChecksFailed are the checks that held and the ones that did not, over all the cases (the sums of
	// KnowledgeCaseRunMeta.PassedChecks and Problems).
	ChecksPassed int `json:"checks_passed"`
	ChecksFailed int `json:"checks_failed"`
}

// EvalType implements EvalRunMeta.
func (KnowledgeEvalRunMeta) EvalType() Type { return TypeKnowledge }

// TrainingEvalRunMeta is the metadata of a training EvalRun (table gb_neo_eval_runs_training_meta): the model that was asked,
// the judge, and the result of the run as a whole. The counters are rewritten after every judge batch, so a running eval run
// shows its progress.
//
// The questions asked are the Counts.Total of the EvalRun and the ones where the model or the judge errored its Counts.Errored.
// Every question that is not errored is Answered; the Judged ones are scored: NumCorrect + NumPartial + NumIncorrect = NumJudged,
// split by the judge's score (>= 4, 2 to 3, 0 to 1). The EvalRun's CostUSD is the judge's cost (the cases of a training eval cost
// nothing by themselves: the judge is called per batch), which is JudgeCostUSD.
type TrainingEvalRunMeta struct {
	EvalRunID EvalRunID `json:"eval_run_id"`
	// TrainedModelID is the trained model that was asked. It is not a foreign key: the trained models are not part of this schema.
	TrainedModelID int64 `json:"trained_model_id"`
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
	NumBatches        int     `json:"num_batches"`
	JudgeCalls        int     `json:"judge_calls"`
	JudgeInputTokens  int     `json:"judge_input_tokens"`
	JudgeOutputTokens int     `json:"judge_output_tokens"`
	JudgeCostUSD      float64 `json:"judge_cost_usd"`
}

// EvalType implements EvalRunMeta.
func (TrainingEvalRunMeta) EvalType() Type { return TypeTraining }
