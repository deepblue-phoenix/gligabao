package training_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/evals"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
	"github.com/deepblue-phoenix/gligabao/internal/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"

	_ "modernc.org/sqlite"
)

func questions(n int) []training.Question {
	out := make([]training.Question, n)
	for i := range out {
		out[i] = training.Question{Instruction: fmt.Sprintf("q%d", i), Reference: fmt.Sprintf("a%d", i)}
	}
	return out
}

// echoGen answers "a<i>" for "q<i>" (a perfect model), except for the prompts in wrong, which get "nope".
func echoGen(wrong ...string) training.GenerateFunc {
	return func(_ context.Context, prompt string) (training.Generation, error) {
		for _, w := range wrong {
			if prompt == w {
				return training.Generation{Response: "nope", InputTokens: 3, OutputTokens: 1}, nil
			}
		}
		return training.Generation{Response: "a" + strings.TrimPrefix(strings.SplitN(prompt, "\n", 2)[0], "q"), InputTokens: 3, OutputTokens: 2}, nil
	}
}

// fakeJudge scores 5 when the answer equals the reference and 0 otherwise.
type fakeJudge struct {
	mu      sync.Mutex
	sizes   []int
	failing bool
	// reply, if set, is the reply text for the call (1-based) instead of the verdicts.
	reply func(call int, verdicts []map[string]any) string
}

func (j *fakeJudge) Complete(_ context.Context, req ai.Request) (*ai.Response, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failing {
		return nil, errors.New("api down")
	}
	content := req.Messages[0].Content
	var cases []struct {
		ID                int
		Reference, Answer string
	}
	if err := json.Unmarshal([]byte(content[strings.Index(content, "["):]), &cases); err != nil {
		return nil, err
	}
	j.sizes = append(j.sizes, len(cases))
	var vs []map[string]any
	for _, c := range cases {
		score := 0
		if c.Answer == c.Reference {
			score = 5
		}
		vs = append(vs, map[string]any{"id": c.ID, "score": score, "reason": "r", "reference": c.Reference})
	}
	b, _ := json.Marshal(vs)
	text := string(b)
	if j.reply != nil {
		text = j.reply(len(j.sizes), vs)
	}
	return &ai.Response{Text: text, Model: "judge-1", InputTokens: 100, OutputTokens: 10, CostUSD: 0.01}, nil
}

type collected struct {
	cases []training.CaseResult
	last  training.Progress
	calls int
}

func (c *collected) sink(_ context.Context, cases []training.CaseResult, p training.Progress) error {
	c.cases = append(c.cases, cases...)
	c.last = p
	c.calls++
	return nil
}

func TestRunBatchesJudgeCalls(t *testing.T) {
	j := &fakeJudge{}
	var c collected
	p, err := training.Run(context.Background(), training.Config{BatchSize: 5}, questions(12), echoGen("q3", "q11"), j, c.sink)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(j.sizes) != "[5 5 2]" || c.calls != 3 || len(c.cases) != 12 {
		t.Errorf("judge batches %v, sink calls %d, cases %d", j.sizes, c.calls, len(c.cases))
	}
	if p != c.last || p.Total != 12 || p.Answered != 12 || p.Judged != 12 || p.Correct != 10 || p.Incorrect != 2 || p.ScoreSum != 50 || p.Batches != 3 {
		t.Errorf("progress: %+v", p)
	}
	if p.JudgeCalls != 3 || p.JudgeCostUSD < 0.0299 || p.JudgeCostUSD > 0.0301 || p.JudgeModel != "judge-1" {
		t.Errorf("judge usage: %+v", p)
	}
	for i, it := range c.cases {
		if it.Idx != i || it.Status != training.CaseJudged || it.Score == nil || it.StartedAt.IsZero() {
			t.Errorf("case %d: %+v", i, it)
		}
	}
}

func TestRunErroredCases(t *testing.T) {
	// The model fails on q1; the judge never gives a verdict for q2.
	gen := func(ctx context.Context, prompt string) (training.Generation, error) {
		if prompt == "q1" {
			return training.Generation{}, errors.New("boom")
		}
		return echoGen()(ctx, prompt)
	}
	j := &fakeJudge{reply: func(_ int, vs []map[string]any) string {
		var keep []map[string]any
		for _, v := range vs {
			if v["reference"] != "a2" {
				keep = append(keep, v)
			}
		}
		b, _ := json.Marshal(keep)
		return string(b)
	}}
	var c collected
	p, err := training.Run(context.Background(), training.Config{BatchSize: 4}, questions(4), gen, j, c.sink)
	if err != nil {
		t.Fatal(err)
	}
	if p.Errored != 2 || p.Judged != 2 || p.Answered != 3 {
		t.Errorf("progress: %+v", p)
	}
	if c.cases[1].Status != training.CaseErrored || !strings.Contains(c.cases[1].Error, "model: boom") ||
		c.cases[2].Status != training.CaseErrored || !strings.Contains(c.cases[2].Error, "judge:") {
		t.Errorf("cases: %+v", c.cases)
	}
}

func TestRunGivesUp(t *testing.T) {
	var c collected
	_, err := training.Run(context.Background(), training.Config{BatchSize: 2}, questions(10),
		func(context.Context, string) (training.Generation, error) {
			return training.Generation{}, errors.New("sidecar down")
		},
		&fakeJudge{}, c.sink)
	if err == nil || !strings.Contains(err.Error(), "sidecar down") || c.calls != 2 {
		t.Errorf("model down: err=%v, batches %d", err, c.calls)
	}
	c = collected{}
	_, err = training.Run(context.Background(), training.Config{BatchSize: 2}, questions(10), echoGen(), &fakeJudge{failing: true}, c.sink)
	if err == nil || !strings.Contains(err.Error(), "api down") || c.calls != 3 {
		t.Errorf("judge down: err=%v, batches %d", err, c.calls)
	}
}

func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var c collected
	gen := func(ctx context.Context, prompt string) (training.Generation, error) {
		if prompt == "q3" {
			cancel()
		}
		return echoGen()(ctx, prompt)
	}
	p, err := training.Run(ctx, training.Config{BatchSize: 2}, questions(10), gen, &fakeJudge{}, c.sink)
	if !errors.Is(err, context.Canceled) || c.calls != 1 || p.Judged != 2 {
		t.Errorf("err=%v batches %d progress %+v", err, c.calls, p)
	}
}

func TestConfigNormalize(t *testing.T) {
	if c, err := (training.Config{}).Normalize(); err != nil || c.BatchSize != training.DefaultBatchSize {
		t.Errorf("default: %+v %v", c, err)
	}
	for _, n := range []int{-1, training.MaxBatchSize + 1} {
		if _, err := (training.Config{BatchSize: n}).Normalize(); err == nil {
			t.Errorf("accepted batch size %d", n)
		}
	}
}

func TestBandOf(t *testing.T) {
	for score, want := range map[int]training.Band{0: training.BandIncorrect, 1: training.BandIncorrect, 2: training.BandPartial, 3: training.BandPartial, 4: training.BandCorrect, 5: training.BandCorrect} {
		if got := training.BandOf(score); got != want {
			t.Errorf("BandOf(%d) = %s, want %s", score, got, want)
		}
	}
}

func TestLoadEvals(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.json", `{"name": "ev-a", "description": "about a", "cases": [{"name": "c1", "instruction": "q1", "output": "a1"}, {"name": "c2", "instruction": "q2", "input": "ctx", "output": "a2"}]}`)
	write("b.json", `{"name": "ev-b", "cases": [{"name": "c3", "instruction": "q3", "output": "a3"}]}`)
	write("notes.txt", `ignored`)
	evals, err := training.LoadEvals(dir)
	if err != nil || len(evals) != 2 || evals[0].Name != "ev-a" || len(evals[0].Cases) != 2 || evals[0].Cases[1].Input != "ctx" || evals[0].Description != "about a" {
		t.Fatalf("LoadEvals: %+v %v", evals, err)
	}
	sel, err := training.SelectEvals(evals, "ev-a", []string{"c2"})
	if err != nil || len(sel) != 1 || len(sel[0].Cases) != 1 || sel[0].Cases[0].Name != "c2" {
		t.Errorf("SelectEvals: %+v %v", sel, err)
	}
	if _, err := training.SelectEvals(evals, "nope", nil); err == nil {
		t.Error("accepted an unknown eval")
	}
	if _, err := training.SelectEvals(evals, "ev-a", []string{"c3"}); err == nil {
		t.Error("accepted a case of another eval")
	}
	spec := training.Spec(evals[0])
	if spec.Type != core.TypeTraining || len(spec.Cases) != 2 || spec.Cases[1].Meta.(*core.TrainingCaseDefMeta).Input != "ctx" {
		t.Errorf("Spec: %+v", spec)
	}

	write("c.json", `{"name": "ev-c", "cases": [{"name": "c1", "instruction": "q", "output": "a"}]}`)
	if _, err := training.LoadEvals(dir); err == nil || !strings.Contains(err.Error(), "duplicate case name") {
		t.Errorf("duplicate case name: %v", err)
	}
	write("c.json", `{"name": "ev-c", "cases": [{"name": "c9", "instruction": "q"}]}`)
	if _, err := training.LoadEvals(dir); err == nil {
		t.Error("accepted a case without an output")
	}
}

func TestExecutor(t *testing.T) {
	ctx := context.Background()
	d, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := evals.Open(config.DBBackendSQLite, d); err != nil {
		t.Fatal(err)
	}
	store, _ := evals.StoreFor(config.DBBackendSQLite)
	r := runner.New(d, store)

	ev := training.Eval{Name: "ds"}
	for i, q := range questions(7) {
		ev.Cases = append(ev.Cases, core.TrainingCase{Name: fmt.Sprintf("c%d", i), TrainingInput: core.TrainingInput{Instruction: q.Instruction}, TrainingExpected: core.TrainingExpected{Output: q.Reference}})
	}
	def, err := r.SyncEval(ctx, training.Spec(ev))
	if err != nil {
		t.Fatal(err)
	}

	var batches, answers int
	x := &training.Executor{
		Config:   training.Config{BatchSize: 3},
		Generate: echoGen("q1", "q5"),
		Judge:    &fakeJudge{},
		OnAnswer: func(training.CaseResult) { answers++ },
		OnBatch:  func([]training.CaseResult, training.Progress) { batches++ },
	}
	run, err := r.RunEval(ctx, def, x, runner.EvalOptions{Meta: &core.TrainingEvalRunMeta{TrainedModelID: 4}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (core.Counts{Total: 7, Completed: 7, Passed: 5, Failed: 2}); run.Counts != want || run.Status != core.StatusCompleted {
		t.Errorf("eval run: %+v", run)
	}
	if batches != 3 || answers != 7 {
		t.Errorf("batches %d, answers %d", batches, answers)
	}
	if run.CostUSD < 0.0299 || run.CostUSD > 0.0301 {
		t.Errorf("cost %v", run.CostUSD)
	}
	// The eval run's metadata agrees with the counts, as core says it does.
	var model, judged, correct, partial, incorrect, sum, nb int
	var judge string
	if err := d.QueryRow(`SELECT trained_model_id, judge_model, num_judged, num_correct, num_partial, num_incorrect, score_sum, num_batches
		FROM gb_eval_runs_meta_training WHERE eval_run_id = ?`, run.ID).Scan(&model, &judge, &judged, &correct, &partial, &incorrect, &sum, &nb); err != nil {
		t.Fatal(err)
	}
	if model != 4 || judge != "judge-1" || judged != 7 || correct != run.Counts.Passed || partial+incorrect != run.Counts.Failed || sum != 25 || nb != 3 {
		t.Errorf("meta: model %d judge %q judged %d correct %d partial %d incorrect %d sum %d batches %d", model, judge, judged, correct, partial, incorrect, sum, nb)
	}
	// A case run of a wrong answer: failed, score 0, with the metadata of what was asked.
	var verdict, response, instruction string
	var score float64
	var js, batch int
	if err := d.QueryRow(`SELECT c.verdict, c.score, m.response, m.instruction, m.judge_score, m.batch FROM gb_case_runs c
		JOIN gb_case_runs_meta_training m ON m.case_run_id = c.id JOIN gb_case_defs cd ON cd.id = c.def_id
		WHERE c.eval_run_id = ? AND cd.name = 'c5'`, run.ID).Scan(&verdict, &score, &response, &instruction, &js, &batch); err != nil {
		t.Fatal(err)
	}
	if verdict != "fail" || score != 0 || response != "nope" || instruction != "q5" || js != 0 || batch != 2 {
		t.Errorf("case run c5: %s %v %q %q %d %d", verdict, score, response, instruction, js, batch)
	}

	// A model that is down errors the run, and the cases that did not get an answer are errored case runs.
	down := &training.Executor{Config: training.Config{BatchSize: 2}, Judge: &fakeJudge{},
		Generate: func(context.Context, string) (training.Generation, error) {
			return training.Generation{}, errors.New("sidecar down")
		}}
	run, err = r.RunEval(ctx, def, down, runner.EvalOptions{Meta: &core.TrainingEvalRunMeta{TrainedModelID: 4}})
	if err == nil || run.Status != core.StatusErrored || !strings.Contains(run.Error, "sidecar down") || run.Counts.Errored != 4 || run.Counts.Pending != 3 {
		t.Errorf("errored run: %+v %v", run, err)
	}

	// Without the metadata of the trained model there is no run.
	if _, err := r.RunEval(ctx, def, x, runner.EvalOptions{}); err == nil {
		t.Error("ran without a TrainingEvalRunMeta")
	}
}
