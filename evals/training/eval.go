package training

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Defaults and limits of Config.
const (
	DefaultBatchSize = 10
	MaxBatchSize     = 25
)

// Statuses of an Item.
const (
	ItemJudged = "judged" // the model answered and the judge scored the answer
	ItemFailed = "failed" // the model or the judge failed: Error says why
)

const (
	// judgeAttempts is how many times a batch (or what is left of it) is sent to the judge.
	judgeAttempts = 2
	// A run that only gets failures stops instead of running on: after this many batches in a row where every
	// answer failed, or where the judge could not grade anything (a dead sidecar, a bad API key).
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
	// BatchSize is how many question/answer pairs go to the judge in one call (default DefaultBatchSize, at most
	// MaxBatchSize). The answers of a batch are generated first, then the whole batch is judged with a single LLM call.
	BatchSize int
	// JudgeModel is the judge's model; empty uses the LLM's default.
	JudgeModel string
	// OnAnswer, if set, is called with each item as soon as the model has answered its question (or failed to), before the
	// batch is judged: the item has no Score yet, and Status is ItemFailed if the model failed, "" otherwise. It lets a
	// caller show the model's behaviour live, ahead of the verdicts that Sink gets per batch. It runs on the run's
	// goroutine, so it must not block for long.
	OnAnswer func(Item)
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

// Item is the outcome for one example.
type Item struct {
	// Idx is the example's position in the dataset's examples that the run was given (0-based), Batch the judge batch
	// it was in (1-based).
	Idx, Batch  int
	Instruction string
	Input       string
	// Reference is the answer in the dataset, Response what the model answered ("" if it failed).
	Reference string
	Response  string
	Status    string
	Error     string
	// Score (MinScore..MaxScore) and Reason are the judge's; nil / "" unless Status is ItemJudged.
	Score  *int
	Reason string
	// InputTokens, OutputTokens and DurationMS are the model's: the token counts it reported and how long it took.
	InputTokens, OutputTokens int
	DurationMS                int64
}

// Progress is how far a run is. Every example is Answered (the model replied), Judged (and scored) or Failed (the model
// or the judge failed); Correct + Partial + Incorrect = Judged.
type Progress struct {
	Total     int
	Answered  int
	Judged    int
	Failed    int
	Correct   int
	Partial   int
	Incorrect int
	// ScoreSum is the sum of the scores of the Judged ones.
	ScoreSum int
	// Batches is the number of judge batches done.
	Batches int
	// JudgeModel is the model that judged (as the LLM reported it), and what the judge cost so far.
	JudgeModel        string
	JudgeCalls        int
	JudgeInputTokens  int
	JudgeOutputTokens int
	JudgeCostUSD      float64
}

// Done is the number of examples that were handled, one way or the other.
func (p Progress) Done() int { return p.Judged + p.Failed }

// MeanScore is the mean score of the judged examples (MinScore..MaxScore), 0 if there are none.
func (p Progress) MeanScore() float64 {
	if p.Judged == 0 {
		return 0
	}
	return float64(p.ScoreSum) / float64(p.Judged)
}

// PassRate is the share of the judged examples that are correct (0..1), 0 if there are none.
func (p Progress) PassRate() float64 {
	if p.Judged == 0 {
		return 0
	}
	return float64(p.Correct) / float64(p.Judged)
}

// Sink receives the items of every finished batch with the progress after it. An error from it stops the run.
type Sink func(ctx context.Context, items []Item, p Progress) error

// Run puts the examples to the model and has the judge grade the answers. The examples go in batches of cfg.BatchSize:
// the model answers the questions of a batch (one at a time: parallel calls can start more GPU containers that each load the model), then ONE call to the judge grades all of them,
// so n examples cost about n/BatchSize judge calls. Each finished batch is handed to sink. A failure of one example (the
// model errors, the judge gives no verdict) marks that item failed and the run goes on; a run that only gets failures
// stops with an error. When ctx ends the run stops with its error, without reporting the batch it was in. The Progress
// reached is returned along with any error.
func Run(ctx context.Context, cfg Config, examples []Example, gen GenerateFunc, judge LLM, sink Sink) (Progress, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return Progress{}, err
	}
	p := Progress{Total: len(examples)}
	var badAnswerBatches, badJudgeBatches int
	for start, batchNo := 0, 1; start < len(examples); start, batchNo = start+cfg.BatchSize, batchNo+1 {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		end := min(start+cfg.BatchSize, len(examples))
		items := answerBatch(ctx, examples[start:end], start, batchNo, gen, cfg.OnAnswer)
		if err := ctx.Err(); err != nil {
			return p, err
		}
		answered := 0
		for _, it := range items {
			if it.Status != ItemFailed {
				answered++
			}
		}
		p.Answered += answered
		judgeErr := judgeBatch(ctx, cfg, judge, items, &p)
		if err := ctx.Err(); err != nil {
			p.Answered -= answered // the batch is not reported
			return p, err
		}
		p.Batches++
		var lastErr string
		for i := range items {
			it := &items[i]
			if it.Status == ItemFailed {
				p.Failed++
				lastErr = it.Error
				continue
			}
			it.Status = ItemJudged
			p.Judged++
			p.ScoreSum += *it.Score
			switch VerdictOf(*it.Score) {
			case VerdictCorrect:
				p.Correct++
			case VerdictPartial:
				p.Partial++
			default:
				p.Incorrect++
			}
		}
		if err := sink(ctx, items, p); err != nil {
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

// answerBatch asks the model the questions of examples (the first one is example number first), one after the other. An
// item whose question failed has Status ItemFailed. When ctx ends, the questions not asked yet fail with its error.
func answerBatch(ctx context.Context, examples []Example, first, batchNo int, gen GenerateFunc, onAnswer func(Item)) []Item {
	items := make([]Item, len(examples))
	for i, ex := range examples {
		it := &items[i]
		*it = Item{Idx: first + i, Batch: batchNo, Instruction: ex.Instruction, Input: ex.Input, Reference: ex.Output}
		if err := ctx.Err(); err != nil {
			it.Status, it.Error = ItemFailed, "model: "+err.Error()
			continue
		}
		t0 := time.Now()
		g, err := gen(ctx, ex.Prompt())
		it.DurationMS = time.Since(t0).Milliseconds()
		if err != nil {
			it.Status, it.Error = ItemFailed, "model: "+err.Error()
		} else {
			it.Response, it.InputTokens, it.OutputTokens = g.Response, g.InputTokens, g.OutputTokens
		}
		if onAnswer != nil && ctx.Err() == nil {
			onAnswer(*it)
		}
	}
	return items
}

// judgeBatch grades the answered items of a batch with one call to the judge (a second one for the items the first call
// left without a verdict), filling in their Score and Reason; the ones it cannot grade become failed. The judge's usage is
// added to p. It returns the last error of the judge if some item could not be graded because of it, nil otherwise.
func judgeBatch(ctx context.Context, cfg Config, judge LLM, items []Item, p *Progress) error {
	var pending []int // indices into items
	for i := range items {
		if items[i].Status != ItemFailed {
			pending = append(pending, i)
		}
	}
	var lastErr error
	for attempt := 0; attempt < judgeAttempts && len(pending) > 0 && ctx.Err() == nil; attempt++ {
		send := make([]judgeItem, len(pending))
		for k, i := range pending {
			send[k] = judgeItem{ID: k + 1, Question: items[i].Instruction, Reference: items[i].Reference, Answer: items[i].Response}
			if items[i].Input != "" {
				send[k].Question += "\n\n" + items[i].Input
			}
		}
		verdicts, resp, err := grade(ctx, judge, send, cfg.JudgeModel)
		if resp != nil {
			p.JudgeCalls++
			p.JudgeInputTokens += resp.InputTokens
			p.JudgeOutputTokens += resp.OutputTokens
			p.JudgeCostUSD += resp.CostUSD
			if p.JudgeModel == "" {
				p.JudgeModel = resp.Model
			}
		}
		lastErr = err
		var left []int
		for k, i := range pending {
			if v, ok := verdicts[k+1]; ok {
				score := v.Score
				items[i].Score, items[i].Reason = &score, v.Reason
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
		items[i].Status = ItemFailed
		switch {
		case lastErr != nil:
			items[i].Error = "judge: " + lastErr.Error()
		default:
			items[i].Error = "judge: no verdict returned for this item"
		}
	}
	if len(pending) > 0 && lastErr == nil {
		lastErr = errors.New("the judge returned no verdict for some items")
	}
	if len(pending) == 0 {
		return nil
	}
	return lastErr
}
