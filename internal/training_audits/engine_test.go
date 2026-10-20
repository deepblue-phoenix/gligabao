package training_audits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/core"
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
	cases []core.AuditCaseRun
	last  Progress
	calls int
}

func (c *collected) sink(_ context.Context, cases []core.AuditCaseRun, p Progress) error {
	c.cases = append(c.cases, cases...)
	c.last = p
	c.calls++
	return nil
}

func TestRunBatches(t *testing.T) {
	j := &fakeJudge{}
	var c collected
	var answered []core.AuditCaseRun
	cfg := Config{BatchSize: 2, OnAnswer: func(it core.AuditCaseRun) { answered = append(answered, it) }}
	p, err := Run(context.Background(), cfg, examples(5), echoGen("q3"), j, c.sink)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(j.sizes) != "[2 2 1]" || c.calls != 3 || len(c.cases) != 5 {
		t.Fatalf("judge sizes %v, %d sink calls, %d cases", j.sizes, c.calls, len(c.cases))
	}
	wantCounts := core.Counts{Total: 5, Completed: 5}
	if p.Counts != wantCounts || p.NumAnswered != 5 || p.NumJudged != 5 || p.NumCorrect != 4 || p.NumIncorrect != 1 || p.NumPartial != 0 ||
		p.ScoreSum != 20 || p.NumBatches != 3 || p.JudgeModel != "judge-1" || p.JudgeCalls != 3 || p.JudgeInputTokens != 300 || p.JudgeOutputTokens != 30 {
		t.Fatalf("progress: %+v", p)
	}
	if p.CostUSD < 0.0299 || p.CostUSD > 0.0301 || p.MeanScore() != 4 || p.CorrectRate() != 0.8 || p.Done() != 5 {
		t.Fatalf("derived: %+v mean %v rate %v", p, p.MeanScore(), p.CorrectRate())
	}
	for i, it := range c.cases {
		wantScore := 5
		if i == 3 {
			wantScore = 0
		}
		if it.Idx != i || it.Status != core.StatusCompleted || it.JudgeScore == nil || *it.JudgeScore != wantScore || it.Batch != i/2+1 ||
			it.Reference != fmt.Sprintf("a%d", i) || it.InputTokens != 3 || it.CreatedAt.IsZero() || it.RunID != 0 {
			t.Errorf("case %d: %+v", i, it)
		}
	}
	if len(answered) != 5 || answered[0].Status != core.StatusRunning || answered[0].JudgeScore != nil || answered[0].Response != "a0" {
		t.Errorf("OnAnswer: %+v", answered)
	}
	// The judge got the question, with the input after a blank line, and the settings.
	if got := j.reqs[0].Messages[0].Content; !strings.Contains(got, `"question": "q0"`) || j.reqs[0].Reasoning != ai.ReasoningNone {
		t.Errorf("judge request: %s", got)
	}
}

func TestRunProgressAddsUpAtEveryBatch(t *testing.T) {
	var seen []Progress
	sink := func(_ context.Context, _ []core.AuditCaseRun, p Progress) error { seen = append(seen, p); return nil }
	if _, err := Run(context.Background(), Config{BatchSize: 3}, examples(7), echoGen("q1"), &fakeJudge{}, sink); err != nil {
		t.Fatal(err)
	}
	for i, p := range seen {
		c := p.Counts
		if c.Total != 7 || c.Pending+c.Running+c.Completed+c.Errored+c.Cancelled != 7 || p.NumCorrect+p.NumPartial+p.NumIncorrect != p.NumJudged ||
			p.NumJudged != c.Completed || p.NumBatches != i+1 {
			t.Errorf("batch %d: %+v", i+1, p)
		}
	}
	if seen[0].Counts.Pending != 4 {
		t.Errorf("pending after the first batch: %+v", seen[0].Counts)
	}
}

func TestRunInputIsPartOfQuestion(t *testing.T) {
	j := &fakeJudge{}
	var asked string
	gen := func(_ context.Context, prompt string) (Generation, error) {
		asked = prompt
		return Generation{Response: "x"}, nil
	}
	ex := []Example{{Instruction: "what?", Input: "ctx", Output: "x"}}
	var c collected
	if _, err := Run(context.Background(), Config{}, ex, gen, j, c.sink); err != nil {
		t.Fatal(err)
	}
	if asked != "what?\n\nctx" || !strings.Contains(j.reqs[0].Messages[0].Content, `"question": "what?\n\nctx"`) {
		t.Errorf("asked %q, judge got %s", asked, j.reqs[0].Messages[0].Content)
	}
	if it := c.cases[0]; it.Instruction != "what?" || it.Input != "ctx" || *it.JudgeScore != 5 {
		t.Errorf("case: %+v", it)
	}
}

func TestRunModelFailureErrorsOnlyThatCase(t *testing.T) {
	gen := func(ctx context.Context, prompt string) (Generation, error) {
		if prompt == "q1" {
			return Generation{}, errors.New("out of memory")
		}
		return echoGen()(ctx, prompt)
	}
	var c collected
	p, err := Run(context.Background(), Config{BatchSize: 3}, examples(3), gen, &fakeJudge{}, c.sink)
	if err != nil {
		t.Fatal(err)
	}
	it := c.cases[1]
	if it.Status != core.StatusErrored || it.Error != "model: out of memory" || it.JudgeScore != nil || it.Batch != 0 {
		t.Errorf("failed case: %+v", it)
	}
	if p.Counts.Completed != 2 || p.Counts.Errored != 1 || p.NumAnswered != 2 || p.NumJudged != 2 {
		t.Errorf("progress: %+v", p)
	}
}

func TestRunJudgeRetriesWhatItMissed(t *testing.T) {
	j := &fakeJudge{mangle: func(call int, vs []map[string]any) string {
		if call == 1 { // drops the second verdict, and gives an out-of-range score for the third
			vs[2]["score"] = 9
			b, _ := json.Marshal([]any{vs[0]})
			return "Here you go:\n```json\n" + string(b) + "\n```"
		}
		b, _ := json.Marshal(vs)
		return string(b)
	}}
	var c collected
	p, err := Run(context.Background(), Config{BatchSize: 3}, examples(3), echoGen(), j, c.sink)
	if err != nil || fmt.Sprint(j.sizes) != "[3 2]" || p.JudgeCalls != 2 || p.NumJudged != 3 || p.Counts.Errored != 0 {
		t.Fatalf("%v sizes %v progress %+v", err, j.sizes, p)
	}
}

func TestRunJudgeFailureAndGivingUp(t *testing.T) {
	// A judge that is down: cases error, the run gives up after maxFailedJudgeBatches batches in a row.
	j := &fakeJudge{failing: true}
	var c collected
	p, err := Run(context.Background(), Config{BatchSize: 2}, examples(20), echoGen(), j, c.sink)
	if err == nil || !strings.Contains(err.Error(), "judge failed to grade 3 batches in a row") || !strings.Contains(err.Error(), "api down") {
		t.Fatalf("err = %v", err)
	}
	if c.calls != 3 || p.Counts.Errored != 6 || p.Counts.Pending != 14 || p.Counts.Completed != 0 || p.NumAnswered != 6 {
		t.Errorf("%d sink calls, progress %+v", c.calls, p)
	}
	if it := c.cases[0]; it.Status != core.StatusErrored || !strings.HasPrefix(it.Error, "judge: ") || it.Response != "a0" {
		t.Errorf("case: %+v", it)
	}

	// A model that always fails: gives up after maxFailedAnswerBatches.
	gen := func(context.Context, string) (Generation, error) { return Generation{}, errors.New("no gpu") }
	c = collected{}
	if p, err = Run(context.Background(), Config{BatchSize: 2}, examples(20), gen, &fakeJudge{}, c.sink); err == nil ||
		!strings.Contains(err.Error(), "model failed to answer 2 batches in a row") || p.Counts.Errored != 4 || c.calls != 2 {
		t.Fatalf("err = %v, progress %+v", err, p)
	}
}

func TestRunStopsOnSinkError(t *testing.T) {
	boom := errors.New("disk full")
	_, err := Run(context.Background(), Config{BatchSize: 2}, examples(6), echoGen(), &fakeJudge{},
		func(context.Context, []core.AuditCaseRun, Progress) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunCancelledInABatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The run is cancelled while the model answers the 4th question, the first of the second batch: the first batch is done, and
	// of the second only q3 (in flight) was reached.
	gen := func(ctx context.Context, prompt string) (Generation, error) {
		if prompt == "q3" {
			cancel()
			return Generation{}, ctx.Err()
		}
		return echoGen()(ctx, prompt)
	}
	var c collected
	p, err := Run(ctx, Config{BatchSize: 3}, examples(8), gen, &fakeJudge{}, c.sink)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(c.cases) != 4 { // q0..q2 judged in the first batch, then q3 was reached, q4 and q5 were not
		t.Fatalf("%d cases", len(c.cases))
	}
	if c.calls != 2 || c.cases[3].Idx != 3 || c.cases[3].Status != core.StatusCancelled || c.cases[3].JudgeScore != nil || c.cases[3].Error != "" {
		t.Fatalf("%d sink calls, cases %+v", c.calls, c.cases)
	}
	want := core.Counts{Total: 8, Pending: 4, Completed: 3, Cancelled: 1}
	if p.Counts != want || c.last.Counts != want || p.NumBatches != 1 || p.NumAnswered != 3 {
		t.Errorf("progress %+v", p)
	}
}

func TestRunCancelledWhileJudging(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	judge := llmFunc(func(context.Context, ai.Request) (*ai.Response, error) {
		cancel()
		return &ai.Response{Text: "[]", Model: "m", InputTokens: 10, OutputTokens: 1, CostUSD: 0.02}, ctx.Err()
	})
	var c collected
	p, err := Run(ctx, Config{BatchSize: 3}, examples(6), echoGen(), judge, c.sink)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(c.cases) != 3 {
		t.Fatalf("%d cases", len(c.cases))
	}
	for _, it := range c.cases {
		if it.Status != core.StatusCancelled || it.JudgeScore != nil || it.Batch != 0 || it.Response == "" {
			t.Errorf("case %+v", it)
		}
	}
	if want := (core.Counts{Total: 6, Pending: 3, Cancelled: 3}); p.Counts != want || p.NumBatches != 0 || p.NumAnswered != 0 || p.NumJudged != 0 {
		t.Errorf("progress %+v", p)
	}
}

type llmFunc func(context.Context, ai.Request) (*ai.Response, error)

func (f llmFunc) Complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	return f(ctx, req)
}

func TestConfigNormalize(t *testing.T) {
	c, err := Config{JudgeModel: "  m "}.Normalize()
	if err != nil || c.BatchSize != DefaultBatchSize || c.JudgeModel != "m" {
		t.Errorf("%+v %v", c, err)
	}
	for _, n := range []int{-1, MaxBatchSize + 1} {
		if _, err := (Config{BatchSize: n}).Normalize(); err == nil {
			t.Errorf("accepted batch size %d", n)
		}
	}
}

func TestParseVerdicts(t *testing.T) {
	ids := map[int]bool{1: true, 2: true, 3: true}
	got, err := parseVerdicts(`ok [{"id":1,"score":4.6,"reason":" fine "},{"id":2,"score":7},{"id":9,"score":3},{"id":1,"score":0},{"id":3}]`, ids)
	if err != nil || len(got) != 1 || got[1].Score != 5 || got[1].Reason != "fine" {
		t.Errorf("%+v %v", got, err)
	}
	for _, bad := range []string{"no array", "[not json]"} {
		if _, err := parseVerdicts(bad, ids); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestVerdictOf(t *testing.T) {
	for score, want := range map[int]string{0: VerdictIncorrect, 1: VerdictIncorrect, 2: VerdictPartial, 3: VerdictPartial, 4: VerdictCorrect, 5: VerdictCorrect} {
		if got := VerdictOf(score); got != want {
			t.Errorf("score %d: %s, want %s", score, got, want)
		}
	}
}

func TestClipKeepsUTF8(t *testing.T) {
	s := strings.Repeat("é", maxJudgeText) // 2 bytes each
	got := clip(s)
	if !strings.HasSuffix(got, " …[cut]") || len(got) > maxJudgeText+10 {
		t.Errorf("len %d", len(got))
	}
	if strings.ToValidUTF8(got, "") != got {
		t.Error("cut a character in two")
	}
}
