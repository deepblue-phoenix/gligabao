package training

import (
	"context"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals_neo/runner"
)

// Executor runs the cases of a training eval against a trained model (it implements runner.Executor). Start the eval run with a
// *core.TrainingEvalRunMeta as EvalOptions.Meta that names the trained model; the Executor keeps it up to date.
type Executor struct {
	// Config says how the run goes (batch size, judge model).
	Config Config
	// Generate asks the trained model.
	Generate GenerateFunc
	// Judge grades the answers.
	Judge LLM
	// OnAnswer, if set, is called with each case as the model answers it (see Config.OnAnswer), before it is judged.
	OnAnswer func(CaseResult)
	// OnBatch, if set, is called with the cases of each batch once they are recorded; names are those of the cases (by Idx).
	OnBatch func(cases []CaseResult, p Progress)
}

// Execute implements runner.Executor. The questions go to the model in the order of def.Cases, and each judged batch is recorded
// as case runs together with the new statistics of the eval run. The run is cost of the judge only.
func (x *Executor) Execute(ctx context.Context, rec *runner.EvalRecorder, def *core.EvalDef) error {
	meta, ok := rec.Run().Meta.(*core.TrainingEvalRunMeta)
	if !ok {
		return fmt.Errorf("a training eval run needs a *core.TrainingEvalRunMeta, not %T", rec.Run().Meta)
	}
	questions := make([]Question, len(def.Cases))
	for i, c := range def.Cases {
		m, ok := c.Meta.(*core.TrainingCaseDefMeta)
		if !ok {
			return fmt.Errorf("case def %q has no training metadata (%T)", c.Name, c.Meta)
		}
		questions[i] = Question{Instruction: m.Instruction, Input: m.Input, Reference: m.Output}
	}
	cfg := x.Config
	cfg.OnAnswer = x.OnAnswer
	// What is recorded must outlive a cancelled run.
	dbCtx := context.WithoutCancel(ctx)
	sink := func(_ context.Context, cases []CaseResult, p Progress) error {
		now := time.Now().UTC().Truncate(time.Second)
		records := make([]runner.CaseRecord, len(cases))
		for i, r := range cases {
			records[i] = caseRecord(def.Cases[r.Idx], r, now)
		}
		meta.JudgeModel = p.JudgeModel
		meta.NumAnswered, meta.NumJudged = p.Answered, p.Judged
		meta.NumCorrect, meta.NumPartial, meta.NumIncorrect = p.Correct, p.Partial, p.Incorrect
		meta.ScoreSum, meta.NumBatches = p.ScoreSum, p.Batches
		meta.JudgeCalls, meta.JudgeInputTokens, meta.JudgeOutputTokens = p.JudgeCalls, p.JudgeInputTokens, p.JudgeOutputTokens
		meta.JudgeCostUSD = p.JudgeCostUSD
		rec.Run().CostUSD = p.JudgeCostUSD // the cases cost nothing by themselves: the judge is called per batch
		if err := rec.RecordCases(dbCtx, records); err != nil {
			return err
		}
		if x.OnBatch != nil {
			x.OnBatch(cases, p)
		}
		return nil
	}
	_, err := Run(ctx, cfg, questions, x.Generate, x.Judge, sink)
	return err
}

// caseRecord is the case run, with its metadata, of a case that has been through the engine. A judged case is completed, with the
// verdict of its score (core.TrainingPassScore) and the score as a share of MaxScore; one that errored has neither.
func caseRecord(def core.CaseDef, r CaseResult, finished time.Time) runner.CaseRecord {
	started := r.StartedAt.UTC().Truncate(time.Second)
	cr := &core.CaseRun{DefID: def.ID, Type: def.Type, Timing: core.Timing{CreatedAt: started, StartedAt: &started, FinishedAt: &finished}}
	meta := &core.TrainingCaseRunMeta{
		Instruction: r.Instruction, Input: r.Input, Reference: r.Reference, Response: r.Response,
		InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, DurationMS: int(r.DurationMS),
	}
	if r.Status == CaseJudged && r.Score != nil {
		cr.Status = core.StatusCompleted
		cr.Verdict = core.VerdictFail
		if *r.Score >= core.TrainingPassScore {
			cr.Verdict = core.VerdictPass
		}
		score := float64(*r.Score) / MaxScore
		cr.Score = &score
		meta.Batch, meta.JudgeScore, meta.JudgeReason = r.Batch, r.Score, r.Reason
	} else {
		cr.Status, cr.Error = core.StatusErrored, r.Error
	}
	return runner.CaseRecord{Run: cr, Meta: meta}
}

const (
	// CallJudge is the type of the runs (gb_inferences) that the judge's LLM calls of a training eval are recorded as; StageJudge is
	// their stage (memory_core.WithStage and memory_core.WithRecording).
	CallJudge  = "training_eval_judge"
	StageJudge = "judge"
)
