package training

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
)

func examples(n int) []Example {
	out := make([]Example, n)
	for i := range out {
		out[i] = Example{Instruction: fmt.Sprintf("q%d", i), Output: fmt.Sprintf("a%d", i)}
	}
	return out
}

// echoGen answers "a<i>" for "q<i>" (a perfect model), except for the prompts in wrong, which get "nope".
func echoGen(wrong ...string) GenerateFunc {
	return func(_ context.Context, prompt string) (Generation, error) {
		for _, w := range wrong {
			if prompt == w {
				return Generation{Response: "nope", InputTokens: 3, OutputTokens: 1}, nil
			}
		}
		return Generation{Response: "a" + strings.TrimPrefix(strings.SplitN(prompt, "\n", 2)[0], "q"), InputTokens: 3, OutputTokens: 2}, nil
	}
}

// fakeJudge scores 5 when the answer equals the reference and 0 otherwise, and counts its calls and the batch sizes.
type fakeJudge struct {
	mu      sync.Mutex
	sizes   []int
	reqs    []ai.Request
	mangle  func(call int, verdicts []map[string]any) string // optional: the reply text from the verdicts
	failing bool
}

func (j *fakeJudge) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failing {
		return nil, errors.New("api down")
	}
	body := req.Messages[0].Content[strings.Index(req.Messages[0].Content, "["):]
	var cases []judgeCase
	if err := json.Unmarshal([]byte(body), &cases); err != nil {
		return nil, err
	}
	j.sizes = append(j.sizes, len(cases))
	j.reqs = append(j.reqs, req)
	var vs []map[string]any
	for _, it := range cases {
		score := 0
		if it.Answer == it.Reference {
			score = 5
		}
		vs = append(vs, map[string]any{"id": it.ID, "score": score, "reason": "r"})
	}
	b, _ := json.Marshal(vs)
	text := string(b)
	if j.mangle != nil {
		text = j.mangle(len(j.sizes), vs)
	}
	return &ai.Response{Text: text, Model: "judge-1", InputTokens: 100, OutputTokens: 10, CostUSD: 0.01}, nil
}

type collected struct {
	cases []CaseResult
	last  Progress
	calls int
}

func (c *collected) sink(_ context.Context, cases []CaseResult, p Progress) error {
	c.cases = append(c.cases, cases...)
	c.last = p
	c.calls++
	return nil
}

func TestRunBatchesJudgeCalls(t *testing.T) {
	j := &fakeJudge{}
	var c collected
	p, err := Run(context.Background(), Config{BatchSize: 5}, examples(12), echoGen("q3", "q11"), j, c.sink)
	if err != nil {
		t.Fatal(err)
	}
	// 12 examples in batches of 5: 3 judge calls (5, 5, 2), not 12.
	if fmt.Sprint(j.sizes) != "[5 5 2]" || c.calls != 3 {
		t.Fatalf("judge batches %v, sink calls %d", j.sizes, c.calls)
	}
	if p.Total != 12 || p.Answered != 12 || p.Judged != 12 || p.Failed != 0 || p.Correct != 10 || p.Incorrect != 2 || p.Partial != 0 {
		t.Fatalf("progress: %+v", p)
	}
	if p.ScoreSum != 50 || p.MeanScore() < 4.16 || p.MeanScore() > 4.17 || p.CorrectRate() < 0.83 || p.CorrectRate() > 0.84 {
		t.Fatalf("score: %+v mean %v correct %v", p, p.MeanScore(), p.CorrectRate())
	}
	if p.JudgeCalls != 3 || p.JudgeModel != "judge-1" || p.JudgeInputTokens != 300 || p.JudgeCostUSD < 0.029 || p.JudgeCostUSD > 0.031 || p.Batches != 3 {
		t.Fatalf("judge usage: %+v", p)
	}
	if len(c.cases) != 12 || c.last != p {
		t.Fatalf("sink: %d cases, last %+v", len(c.cases), c.last)
	}
	for i, it := range c.cases {
		if it.Idx != i || it.Batch != i/5+1 || it.Status != CaseJudged || it.Score == nil || it.Instruction != fmt.Sprintf("q%d", i) ||
			it.Reference != fmt.Sprintf("a%d", i) || it.InputTokens != 3 {
			t.Fatalf("case %d: %+v", i, it)
		}
	}
	if *c.cases[3].Score != 0 || c.cases[3].Response != "nope" || *c.cases[4].Score != 5 {
		t.Fatalf("scores: %+v %+v", c.cases[3], c.cases[4])
	}
}

func TestRunUsesTheTrainingPrompt(t *testing.T) {
	var prompts []string
	gen := func(_ context.Context, prompt string) (Generation, error) {
		prompts = append(prompts, prompt)
		return Generation{Response: "x"}, nil
	}
	ex := []Example{{Instruction: "Where?", Input: "In Paris", Output: "x"}, {Instruction: "Plain", Output: "x"}}
	if _, err := Run(context.Background(), Config{}, ex, gen, &fakeJudge{}, (&collected{}).sink); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%q", prompts) != `["Where?\n\nIn Paris" "Plain"]` {
		t.Fatalf("prompts: %q", prompts)
	}
}

func TestRunItemFailuresDoNotStopTheRun(t *testing.T) {
	gen := func(_ context.Context, prompt string) (Generation, error) {
		if prompt == "q1" {
			return Generation{}, errors.New("boom")
		}
		return echoGen()(context.Background(), prompt)
	}
	j := &fakeJudge{}
	var c collected
	p, err := Run(context.Background(), Config{BatchSize: 3}, examples(3), gen, j, c.sink)
	if err != nil || p.Failed != 1 || p.Judged != 2 || p.Answered != 2 || p.Done() != 3 {
		t.Fatalf("%+v %v", p, err)
	}
	if j.sizes[0] != 2 { // the failed one is not sent to the judge
		t.Fatalf("judge batch sizes %v", j.sizes)
	}
	if it := c.cases[1]; it.Status != CaseFailed || it.Score != nil || !strings.Contains(it.Error, "boom") {
		t.Fatalf("failed case: %+v", it)
	}
}

func TestRunRetriesItemsTheJudgeSkipped(t *testing.T) {
	// The first reply leaves out case 2 and has a bad score for case 3; the retry (of those two) answers.
	j := &fakeJudge{mangle: func(call int, vs []map[string]any) string {
		if call == 1 {
			b, _ := json.Marshal([]map[string]any{vs[0], {"id": 3, "score": 9, "reason": "x"}})
			return "```json\n" + string(b) + "\n```"
		}
		b, _ := json.Marshal(vs)
		return string(b)
	}}
	var c collected
	p, err := Run(context.Background(), Config{BatchSize: 3}, examples(3), echoGen(), j, c.sink)
	if err != nil || p.Judged != 3 || p.Failed != 0 || p.JudgeCalls != 2 || fmt.Sprint(j.sizes) != "[3 2]" {
		t.Fatalf("%+v %v sizes %v", p, err, j.sizes)
	}
}

func TestRunUnjudgeableItemsFail(t *testing.T) {
	j := &fakeJudge{mangle: func(int, []map[string]any) string { return "I cannot do that" }}
	var c collected
	p, err := Run(context.Background(), Config{BatchSize: 2}, examples(2), echoGen(), j, c.sink)
	if err != nil || p.Failed != 2 || p.Judged != 0 || p.JudgeCalls != 2 {
		t.Fatalf("%+v %v", p, err)
	}
	if !strings.Contains(c.cases[0].Error, "judge") {
		t.Fatalf("error: %q", c.cases[0].Error)
	}
}

func TestRunGivesUpWhenTheJudgeIsDown(t *testing.T) {
	j := &fakeJudge{failing: true}
	var c collected
	p, err := Run(context.Background(), Config{BatchSize: 2}, examples(20), echoGen(), j, c.sink)
	if err == nil || !strings.Contains(err.Error(), "api down") || c.calls != maxFailedJudgeBatches || p.Failed != 2*maxFailedJudgeBatches {
		t.Fatalf("%+v %v after %d batches", p, err, c.calls)
	}
}

func TestRunGivesUpWhenTheModelIsDown(t *testing.T) {
	gen := func(context.Context, string) (Generation, error) { return Generation{}, errors.New("sidecar down") }
	j := &fakeJudge{}
	var c collected
	_, err := Run(context.Background(), Config{BatchSize: 2}, examples(20), gen, j, c.sink)
	if err == nil || !strings.Contains(err.Error(), "sidecar down") || c.calls != maxFailedAnswerBatches || len(j.sizes) != 0 {
		t.Fatalf("%v after %d batches, judge calls %v", err, c.calls, j.sizes)
	}
}

func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var c collected
	sink := func(ctx context.Context, cases []CaseResult, p Progress) error {
		err := c.sink(ctx, cases, p)
		cancel() // after the first batch
		return err
	}
	p, err := Run(ctx, Config{BatchSize: 2}, examples(10), echoGen(), &fakeJudge{}, sink)
	if !errors.Is(err, context.Canceled) || c.calls != 1 || p.Judged != 2 {
		t.Fatalf("%+v %v calls %d", p, err, c.calls)
	}
}

// TestRunAsksOneQuestionAtATime checks that the model never gets two questions at once and gets them in dataset order,
// while the judge still gets them in batches.
func TestRunAsksOneQuestionAtATime(t *testing.T) {
	var cur, peak atomic.Int32
	var order []string
	gen := func(ctx context.Context, prompt string) (Generation, error) {
		n := cur.Add(1)
		defer cur.Add(-1)
		if n > peak.Load() {
			peak.Store(n)
		}
		order = append(order, prompt)
		return echoGen()(ctx, prompt)
	}
	j := &fakeJudge{}
	var c collected
	p, err := Run(context.Background(), Config{BatchSize: 8}, examples(16), gen, j, c.sink)
	if err != nil || p.Judged != 16 {
		t.Fatalf("%+v %v", p, err)
	}
	if peak.Load() != 1 || len(order) != 16 || order[0] != "q0" || order[15] != "q15" || fmt.Sprint(j.sizes) != "[8 8]" {
		t.Fatalf("peak %d, order %v, judge batches %v", peak.Load(), order, j.sizes)
	}
}

func TestConfigNormalize(t *testing.T) {
	c, err := Config{JudgeModel: " m "}.Normalize()
	if err != nil || c.BatchSize != DefaultBatchSize || c.JudgeModel != "m" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []Config{{BatchSize: -1}, {BatchSize: MaxBatchSize + 1}} {
		if _, err := bad.Normalize(); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
}

func TestJudgeRequest(t *testing.T) {
	long := strings.Repeat("é", maxJudgeText) // 2 bytes each: the cut must land on a character boundary
	req, err := judgeRequest([]judgeCase{{ID: 1, Question: "q", Reference: long, Answer: "a"}}, "mm")
	if err != nil || req.Model != "mm" || req.MaxTokens < 300 || req.System == "" {
		t.Fatalf("%+v %v", req, err)
	}
	content := req.Messages[0].Content
	if !strings.Contains(content, "…[cut]") {
		t.Fatal("the long text was not cut")
	}
	var cases []judgeCase
	if err := json.Unmarshal([]byte(content[strings.Index(content, "["):]), &cases); err != nil || len(cases) != 1 {
		t.Fatalf("the cases are not valid JSON: %v", err)
	}
}

func TestVerdictOf(t *testing.T) {
	for score, want := range map[int]string{5: VerdictCorrect, 4: VerdictCorrect, 3: VerdictPartial, 2: VerdictPartial, 1: VerdictIncorrect, 0: VerdictIncorrect} {
		if got := VerdictOf(score); got != want {
			t.Errorf("score %d: %s, want %s", score, got, want)
		}
	}
}

func TestParseExamples(t *testing.T) {
	ex, err := ParseExamples([]byte("{\"instruction\":\"a\",\"output\":\"b\"}\n\n{\"instruction\":\"c\",\"input\":\"d\",\"output\":\"e\"}\n"))
	if err != nil || len(ex) != 2 || ex[1].Input != "d" || ex[1].Prompt() != "c\n\nd" || ex[0].Prompt() != "a" {
		t.Fatalf("%+v %v", ex, err)
	}
	for _, bad := range []string{"", "\n\n", "not json", `{"instruction":"","output":"x"}`} {
		if _, err := ParseExamples([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestOnAnswerGetsEachAnswerBeforeItsBatchIsJudged(t *testing.T) {
	var c collected
	var events []string
	cfg := Config{BatchSize: 2, OnAnswer: func(it CaseResult) {
		events = append(events, fmt.Sprintf("answer %d %s", it.Idx, it.Response))
		if it.Score != nil {
			t.Errorf("case %d has a score before the judge ran", it.Idx)
		}
	}}
	sink := func(ctx context.Context, cases []CaseResult, p Progress) error {
		events = append(events, fmt.Sprintf("batch of %d", len(cases)))
		return c.sink(ctx, cases, p)
	}
	gen := func(ctx context.Context, prompt string) (Generation, error) {
		if prompt == "q1" {
			return Generation{}, errors.New("boom")
		}
		return echoGen()(ctx, prompt)
	}
	if _, err := Run(context.Background(), cfg, examples(3), gen, &fakeJudge{}, sink); err != nil {
		t.Fatal(err)
	}
	want := "[answer 0 a0 answer 1  batch of 2 answer 2 a2 batch of 1]"
	if got := fmt.Sprint(events); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
}
