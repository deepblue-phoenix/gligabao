package training_audits

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
)

// The engine: it asks the trained model the examples of its dataset and has the judge grade the answers (Run). It knows nothing of
// HTTP, the sidecar or the database: the model is a function (GenerateFunc), the judge an LLM and the results go to a Sink. The
// Manager wires those up and stores what the engine reports. What it reports are the structs of package core: the case runs
// (without their RunID, which the caller knows) and the Progress, which is what the AuditRun holds of the run so far.

// Defaults and limits of Config.
const (
	DefaultBatchSize = 10
	MaxBatchSize     = 25
)

const (
	// judgeAttempts is how many times a batch (or what is left of it) is sent to the judge.
	judgeAttempts = 2
	// A run that only gets failures stops instead of running on: after this many batches in a row where every answer failed, or
	// where the judge could not grade anything (a dead sidecar, a bad API key).
	maxFailedAnswerBatches = 2
	maxFailedJudgeBatches  = 3
)

// Generation is what the trained model answered to a prompt.
type Generation struct {
	Response     string
	InputTokens  int
	OutputTokens int
}

// GenerateFunc asks the trained model a prompt. Run calls it for one question at a time, never in parallel.
type GenerateFunc func(ctx context.Context, prompt string) (Generation, error)

// Config says how a run goes.
type Config struct {
	// BatchSize is how many question/answer pairs go to the judge in one call (default DefaultBatchSize, at most MaxBatchSize).
	// The answers of a batch are generated first, then the whole batch is judged with a single LLM call.
	BatchSize int
	// JudgeModel is the judge's model; empty uses the LLM's default.
	JudgeModel string
	// OnAnswer, if set, is called with each case as soon as the model has answered its question (or failed to), before the batch
	// is judged: the case has no score yet, and its Status is StatusRunning (answered, awaiting the judge) or StatusErrored if the
	// model failed. It lets a caller show the model's behaviour live, ahead of the verdicts that the Sink gets per batch. It runs
	// on the run's goroutine, so it must not block for long.
	OnAnswer func(core.AuditCaseRun)
}

// Normalize fills in the defaults and checks the limits.
func (c Config) Normalize() (Config, error) {
	if c.BatchSize == 0 {
		c.BatchSize = DefaultBatchSize
	}
	if c.BatchSize < 1 || c.BatchSize > MaxBatchSize {
		return c, fmt.Errorf("batch size must be between 1 and %d", MaxBatchSize)
	}
	c.JudgeModel = strings.TrimSpace(c.JudgeModel)
	return c, nil
}

// Progress is how far a run is: the part of an AuditRun that the engine works out (Apply puts it there).
//
// Every example is in exactly one of Counts: Pending (not reached), Completed (the model answered and the judge scored it),
// Errored (the model or the judge failed) or Cancelled (the run was stopped while it was in the batch). NumAnswered is the number
// of replies of the model in the batches that ended, so it includes the ones the judge then failed to score; the answer a
// cancelled case got is kept in the case but is not counted. NumJudged equals Counts.Completed.
type Progress struct {
	Counts core.Counts
	core.AuditStats
	// CostUSD is what the judge cost so far.
	CostUSD float64
}

// Apply sets the run's Counts, AuditStats and CostUSD to p (its status, error and timing are not touched).
func (p Progress) Apply(r *core.AuditRun) {
	r.Counts, r.AuditStats, r.CostUSD = p.Counts, p.AuditStats, p.CostUSD
}

// Done is the number of examples that were handled, one way or the other.
func (p Progress) Done() int { return p.Counts.Total - p.Counts.Pending }

// MeanScore is the mean score of the judged examples (MinScore..MaxScore), 0 if there are none.
func (p Progress) MeanScore() float64 {
	if p.NumJudged == 0 {
		return 0
	}
	return float64(p.ScoreSum) / float64(p.NumJudged)
}

// CorrectRate is the share of the judged examples that are correct (0..1), 0 if there are none.
func (p Progress) CorrectRate() float64 {
	if p.NumJudged == 0 {
		return 0
	}
	return float64(p.NumCorrect) / float64(p.NumJudged)
}

// Sink receives the case runs of every finished batch with the progress after it. An error from it stops the run. The case runs
// have no ID, no RunID and an Idx that counts from the first example Run was given.
type Sink func(ctx context.Context, cases []core.AuditCaseRun, p Progress) error

// Run puts the examples to the model and has the judge grade the answers. The examples go in batches of cfg.BatchSize: the model
// answers the questions of a batch (one at a time: parallel calls can start more GPU containers that each load the model), then
// ONE call to the judge grades all of them, so n examples cost about n/BatchSize judge calls. Each finished batch is handed to
// sink. A failure of one example (the model errors, the judge gives no verdict) marks that case errored and the run goes on; a
// run that only gets failures stops with an error. When ctx ends the run stops with its error, after handing sink what it has
// of the batch it was in: the cases it had reached, as cancelled (or errored, if they had failed already). The Progress reached
// is returned along with any error.
func Run(ctx context.Context, cfg Config, examples []Example, gen GenerateFunc, judge LLM, sink Sink) (Progress, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return Progress{}, err
	}
	p := Progress{Counts: core.Counts{Total: len(examples), Pending: len(examples)}}
	var badAnswerBatches, badJudgeBatches int
	for start, batchNo := 0, 1; start < len(examples); start, batchNo = start+cfg.BatchSize, batchNo+1 {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		end := min(start+cfg.BatchSize, len(examples))
		cases := answerBatch(ctx, examples[start:end], start, gen, cfg.OnAnswer)
		answered := 0
		for _, it := range cases {
			if it.Status == core.StatusRunning {
				answered++
			}
		}
		var judgeErr error
		if ctx.Err() == nil {
			judgeErr = judgeBatch(ctx, cfg, judge, cases, &p)
		}
		if err := ctx.Err(); err != nil {
			return p, cancelBatch(ctx, cases, &p, sink, err)
		}
		var lastErr string
		for i := range cases {
			it := &cases[i]
			switch it.Status {
			case core.StatusErrored:
				p.Counts.Errored++
				lastErr = it.Error
			default: // answered and scored (StatusRunning until now)
				it.Status, it.Batch = core.StatusCompleted, batchNo
				p.Counts.Completed++
				p.NumJudged++
				p.ScoreSum += *it.JudgeScore
				switch VerdictOf(*it.JudgeScore) {
				case VerdictCorrect:
					p.NumCorrect++
				case VerdictPartial:
					p.NumPartial++
				default:
					p.NumIncorrect++
				}
			}
		}
		p.NumAnswered += answered
		p.NumBatches++
		p.Counts.Pending = p.Counts.Total - p.Counts.Completed - p.Counts.Errored - p.Counts.Cancelled
		if err := sink(ctx, cases, p); err != nil {
			return p, err
		}

		if answered == 0 {
			if badAnswerBatches++; badAnswerBatches >= maxFailedAnswerBatches {
				return p, fmt.Errorf("the model failed to answer %d batches in a row, giving up: %s", badAnswerBatches, lastErr)
			}
		} else {
			badAnswerBatches = 0
		}
		if answered > 0 && judgeErr != nil {
			if badJudgeBatches++; badJudgeBatches >= maxFailedJudgeBatches {
				return p, fmt.Errorf("the judge failed to grade %d batches in a row, giving up: %w", badJudgeBatches, judgeErr)
			}
		} else if answered > 0 {
			badJudgeBatches = 0
		}
	}
	return p, nil
}

// answerBatch asks the model the questions of examples (the first one is example number first), one after the other. A case whose
// question failed is errored. When ctx ends, the case it ends in is cancelled and the questions not asked yet stay pending
// (they are not part of the batch's outcome: see cancelBatch).
func answerBatch(ctx context.Context, examples []Example, first int, gen GenerateFunc, onAnswer func(core.AuditCaseRun)) []core.AuditCaseRun {
	cases := make([]core.AuditCaseRun, len(examples))
	for i, ex := range examples {
		it := &cases[i]
		*it = core.AuditCaseRun{Idx: first + i, Status: core.StatusPending, Instruction: ex.Instruction, Input: ex.Input,
			Reference: ex.Output, CreatedAt: time.Now().UTC()}
		if ctx.Err() != nil {
			continue
		}
		t0 := time.Now()
		g, err := gen(ctx, ex.Prompt())
		it.DurationMS = int(time.Since(t0).Milliseconds())
		switch {
		case ctx.Err() != nil:
			it.Status = core.StatusCancelled
		case err != nil:
			it.Status, it.Error = core.StatusErrored, "model: "+err.Error()
		default:
			it.Status = core.StatusRunning // answered, awaiting the judge
			it.Response, it.InputTokens, it.OutputTokens = g.Response, g.InputTokens, g.OutputTokens
		}
		if onAnswer != nil && ctx.Err() == nil {
			onAnswer(*it)
		}
	}
	return cases
}

// judgeBatch grades the answered cases of a batch (Status running) with one call to the judge (a second one for the cases the
// first call left without a verdict), filling in their JudgeScore and JudgeReason; the ones it cannot grade become errored. The
// judge's usage is added to p. It returns the last error of the judge if some case could not be graded because of it, nil
// otherwise, and ctx's error if ctx ended.
func judgeBatch(ctx context.Context, cfg Config, judge LLM, cases []core.AuditCaseRun, p *Progress) error {
	var pending []int // indices into cases
	for i := range cases {
		if cases[i].Status == core.StatusRunning {
			pending = append(pending, i)
		}
	}
	var lastErr error
	for attempt := 0; attempt < judgeAttempts && len(pending) > 0 && ctx.Err() == nil; attempt++ {
		send := make([]judgeCase, len(pending))
		for k, i := range pending {
			send[k] = judgeCase{ID: k + 1, Question: cases[i].Instruction, Reference: cases[i].Reference, Answer: cases[i].Response}
			if cases[i].Input != "" {
				send[k].Question += "\n\n" + cases[i].Input
			}
		}
		verdicts, resp, err := grade(ctx, judge, send, cfg.JudgeModel)
		if resp != nil {
			p.JudgeCalls++
			p.JudgeInputTokens += resp.InputTokens
			p.JudgeOutputTokens += resp.OutputTokens
			p.CostUSD += resp.CostUSD
			if p.JudgeModel == "" {
				p.JudgeModel = resp.Model
			}
		}
		lastErr = err
		var left []int
		for k, i := range pending {
			if v, ok := verdicts[k+1]; ok {
				score := v.Score
				cases[i].JudgeScore, cases[i].JudgeReason = &score, v.Reason
			} else {
				left = append(left, i)
			}
		}
		pending = left
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, i := range pending {
		cases[i].Status = core.StatusErrored
		switch {
		case lastErr != nil:
			cases[i].Error = "judge: " + lastErr.Error()
		default:
			cases[i].Error = "judge: no verdict returned for this case"
		}
	}
	if len(pending) > 0 && lastErr == nil {
		lastErr = errors.New("the judge returned no verdict for some cases")
	}
	if len(pending) == 0 {
		return nil
	}
	return lastErr
}

// cancelBatch ends a batch that ctx interrupted: it hands sink the cases of the batch that were reached (the ones not asked yet
// stay pending and are not reported), the ones that had not failed already as cancelled, without a score, and the progress,
// which has the judge's usage so far. It returns cause, the error that ends the run.
func cancelBatch(ctx context.Context, cases []core.AuditCaseRun, p *Progress, sink Sink, causeErr error) error {
	var reached []core.AuditCaseRun
	for _, it := range cases {
		switch it.Status {
		case core.StatusPending:
			continue
		case core.StatusErrored:
			p.Counts.Errored++
		default:
			it.Status, it.JudgeScore, it.JudgeReason, it.Batch = core.StatusCancelled, nil, "", 0
			p.Counts.Cancelled++
		}
		reached = append(reached, it)
	}
	p.Counts.Pending = p.Counts.Total - p.Counts.Completed - p.Counts.Errored - p.Counts.Cancelled
	if err := sink(context.WithoutCancel(ctx), reached, *p); err != nil {
		return fmt.Errorf("storing the cases of the interrupted batch: %w", err) // not wrapping cause: this one is not a cancellation
	}
	return causeErr
}
