package training

import (
	"encoding/json"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/core"
)

// The views are the JSON the HTTP API serves of an eval run and its case runs, and that the admin UI (admin_ui/src/eval_live.ts)
// reads: one flat record with the counters of the run (what its Counts and TrainingEvalRunMeta hold), and one per case. The words
// of the statuses are those of the constants below, not those of core (an eval run is "error" here, "errored" in core).

// The statuses of a RunView.
const (
	RunRunning   = "running"
	RunCompleted = "completed" // it ran to the end: it has a score, not a verdict
	RunErrored   = "error"     // the run itself errored: Error says why (the model or the judge kept failing, or a restart interrupted it)
	RunCancelled = "cancelled" // stopped by the user; what was done is kept
)

// The statuses of a CaseView.
const (
	ViewJudged   = "judged" // the model answered and the judge scored it (a score of 0 too)
	ViewErrored  = "error"  // the model or the judge errored: no score, Error says why
	ViewAnswered = "answered"
	// ViewAnswered is never stored: the live feed uses it for a case the model answered whose batch the judge has not scored yet.
)

// KindEval is the Kind of a RunView.
const KindEval = "eval"

// RunView is an eval run of a trained model.
type RunView struct {
	ID             int64  `json:"id"` // the ID of the eval run
	TrainedModelID int64  `json:"trained_model_id"`
	Kind           string `json:"kind"` // KindEval
	Status         string `json:"status"`
	// Eval is the name of the eval whose cases were asked.
	Eval string `json:"eval"`
	// Params are the settings of the run as a JSON object (batch_size, max_tokens, temperature, limit).
	Params json.RawMessage `json:"params"`
	// JudgeModel is the model that judged ("" until its first reply).
	JudgeModel  string `json:"judge_model"`
	NumExamples int    `json:"num_examples"`
	NumAnswered int    `json:"num_answered"`
	NumJudged   int    `json:"num_judged"`
	NumErrored  int    `json:"num_errored"`
	// NumCorrect + NumPartial + NumIncorrect = NumJudged, and ScoreSum is the sum of the scores (0 to 5) of the judged ones.
	NumCorrect   int `json:"num_correct"`
	NumPartial   int `json:"num_partial"`
	NumIncorrect int `json:"num_incorrect"`
	ScoreSum     int `json:"score_sum"`
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

// ViewOfRun is the view of a training eval run of the eval called eval. A run whose Meta is not that of a training eval run has no
// counters of it.
func ViewOfRun(r *core.EvalRun, eval string) RunView {
	v := RunView{ID: int64(r.ID), Kind: KindEval, Status: runStatus(r.Status), Eval: eval, Params: json.RawMessage("{}"),
		NumExamples: r.Counts.Total, NumErrored: r.Counts.Errored, JudgeCostUSD: r.CostUSD, Error: r.Error, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt}
	if r.StartedAt != nil {
		v.CreatedAt = *r.StartedAt
	}
	if len(r.Params) > 0 {
		if b, err := json.Marshal(r.Params); err == nil {
			v.Params = b
		}
	}
	switch m := r.Meta.(type) {
	case *core.TrainingEvalRunMeta:
		v.fill(m)
	case core.TrainingEvalRunMeta:
		v.fill(&m)
	}
	return v
}

func (v *RunView) fill(m *core.TrainingEvalRunMeta) {
	v.TrainedModelID, v.JudgeModel = m.TrainedModelID, m.JudgeModel
	v.NumAnswered, v.NumJudged = m.NumAnswered, m.NumJudged
	v.NumCorrect, v.NumPartial, v.NumIncorrect, v.ScoreSum = m.NumCorrect, m.NumPartial, m.NumIncorrect, m.ScoreSum
	v.NumBatches, v.JudgeCalls, v.JudgeInputTokens, v.JudgeOutputTokens, v.JudgeCostUSD =
		m.NumBatches, m.JudgeCalls, m.JudgeInputTokens, m.JudgeOutputTokens, m.JudgeCostUSD
}

func runStatus(s core.Status) string {
	switch s {
	case core.StatusCompleted:
		return RunCompleted
	case core.StatusErrored:
		return RunErrored
	case core.StatusCancelled:
		return RunCancelled
	}
	return RunRunning // pending too: it is about to run
}

// MeanScore and CorrectRate are the mean judge score (0 to 5) of the judged answers and the share of them that is correct (a score
// of at least core.TrainingPassScore); nil while nothing is judged.
func (v RunView) MeanScore() *float64 {
	if v.NumJudged == 0 {
		return nil
	}
	m := float64(v.ScoreSum) / float64(v.NumJudged)
	return &m
}

func (v RunView) CorrectRate() *float64 {
	if v.NumJudged == 0 {
		return nil
	}
	r := float64(v.NumCorrect) / float64(v.NumJudged)
	return &r
}

// CaseView is a case of an eval run: the question, the expected answer (Reference), the model's (Response) and the judge's score.
type CaseView struct {
	// ID is that of the case run (0 in the live feed, before it is stored) and RunID that of the eval run.
	ID    int64 `json:"id"`
	RunID int64 `json:"run_id"`
	// Idx is the case's position among the cases the run asked (0-based), Batch the judge batch it was graded in (1-based).
	Idx   int `json:"idx"`
	Batch int `json:"batch"`
	// Name is the name of the case within its eval.
	Name        string `json:"name"`
	Instruction string `json:"instruction"`
	Input       string `json:"input"`
	Reference   string `json:"reference"`
	Response    string `json:"response"`
	Status      string `json:"status"` // ViewJudged, ViewErrored or (live feed only) ViewAnswered
	Error       string `json:"error"`
	// Score is the judge's, 0 to 5 (nil unless the case is judged); Reason its explanation.
	Score  *int   `json:"score"`
	Reason string `json:"reason"`
	// InputTokens, OutputTokens and DurationMS are the model's for this question.
	InputTokens  int   `json:"input_tokens"`
	OutputTokens int   `json:"output_tokens"`
	DurationMS   int64 `json:"duration_ms"`
}

// ViewOfResult is the view of a case that went through the engine (the feed's). name is that of the case.
func ViewOfResult(runID core.EvalRunID, name string, r CaseResult) CaseView {
	v := CaseView{RunID: int64(runID), Idx: r.Idx, Batch: r.Batch, Name: name, Instruction: r.Instruction, Input: r.Input, Reference: r.Reference,
		Response: r.Response, Error: r.Error, Score: r.Score, Reason: r.Reason, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, DurationMS: r.DurationMS}
	switch r.Status {
	case CaseJudged:
		v.Status = ViewJudged
	case CaseErrored:
		v.Status = ViewErrored
	default: // answered, not judged yet
		v.Status = ViewAnswered
	}
	return v
}

// ViewOfCaseRun is the view of a stored case run: name is that of its case def and idx its position among the cases of the run.
func ViewOfCaseRun(c *core.CaseRun, name string, idx int) CaseView {
	v := CaseView{ID: int64(c.ID), RunID: int64(c.EvalRunID), Idx: idx, Name: name, Status: ViewErrored, Error: c.Error}
	var m *core.TrainingCaseRunMeta
	switch x := c.Meta.(type) {
	case *core.TrainingCaseRunMeta:
		m = x
	case core.TrainingCaseRunMeta:
		m = &x
	}
	if m != nil {
		v.Batch, v.Instruction, v.Input, v.Reference, v.Response = m.Batch, m.Instruction, m.Input, m.Reference, m.Response
		v.Score, v.Reason = m.JudgeScore, m.JudgeReason
		v.InputTokens, v.OutputTokens, v.DurationMS = m.InputTokens, m.OutputTokens, int64(m.DurationMS)
	}
	if c.Status == core.StatusCompleted && v.Score != nil {
		v.Status = ViewJudged
	}
	return v
}
