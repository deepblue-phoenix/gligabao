package synth

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/llm"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// Request is what Generate is asked to make.
type Request struct {
	Settings    core.SynthSettings
	EvalName    string
	Description string
	// Count is the number of new cases to generate.
	Count int
	// Keep are the cases that stay (the ones of the user): they are not generated again, their queries are shown so the new cases do not repeat them. Their names are taken.
	Keep []core.KnowledgeCase
	// Limits validate every generated case; a case that is not valid is dropped and asked for again.
	Limits evalfile.Limits
}

// Result is the state of a generation: the cases so far and what the calls cost. Generate returns it, and passes it to its progress
// function after every call.
type Result struct {
	// Cases are the new cases, named (CaseName) and valid, at most Request.Count.
	Cases        []core.KnowledgeCase
	Calls        int
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	// Rejected is the number of generated cases that were not valid and dropped.
	Rejected int
}

// extraCalls is how many calls more than the minimum a generation may make, to replace what was invalid or missing.
const extraCalls = 3

// perCall is the number of cases one call asks for in the mode of s.
func perCall(s core.SynthSettings) int {
	if s.Mode == core.SynthBulk {
		return max(1, s.BulkSize)
	}
	return 1
}

// maxCalls is the most calls a generation of count cases makes.
func maxCalls(s core.SynthSettings,
	count int) int {
	n := perCall(s)
	return (count+n-1)/n + extraCalls
}

// maxTokens is the size of the answer asked for n cases.
func maxTokens(n int) int { return min(32000, 1500+2500*n) }

// CaseName is the name of the case number i (from 1).
func CaseName(i int) string { return fmt.Sprintf("case-%03d", i) }

// freeNames returns the first n names CaseName(1), CaseName(2), ... that are not in taken, in order.
func freeNames(taken map[string]bool,
	n int) []string {
	out := make([]string, 0, n)
	for i := 1; len(out) < n; i++ {
		if !taken[CaseName(i)] {
			out = append(out, CaseName(i))
		}
	}
	return out
}

// Generate asks lm for req.Count new cases, one call per case or per bulk, as req.Settings says, and returns them with the cost. It
// stops when it has them, when it has made as many calls as it may (a call whose answer is not valid JSON, or whose cases are not
// valid, is made again), on an error of the LLM, or when ctx is done. What was generated before a failure is in the Result, with the
// error. progress, if not nil, is called with the Result after every call.
func Generate(ctx context.Context,
	lm memory_core.LLM,
	req Request,
	progress func(Result)) (Result, error) {
	if lm == nil {
		return Result{}, core.ErrNoLLM
	}
	taken := map[string]bool{}
	for _, c := range req.Keep {
		taken[c.Name] = true
	}
	return step(ctx,
		lm,
		req,
		freeNames(taken, req.Count),
		progress,
		Result{})
}

// step makes the next call and continues with the new state (the generation is this recursion: the state goes in, the next one comes out).
func step(ctx context.Context,
	lm memory_core.LLM,
	req Request,
	names []string,
	progress func(Result),
	st Result) (Result, error) {
	have := len(st.Cases)
	if have >= req.Count {
		return st, nil
	}
	if st.Calls >= maxCalls(req.Settings, req.Count) {
		return st, fmt.Errorf("generated %d of %d cases: %d calls were not enough",
			have,
			req.Count,
			st.Calls)
	}
	if err := ctx.Err(); err != nil {
		return st, err
	}
	n := min(perCall(req.Settings), req.Count-have)
	resp, err := lm.Complete(ctx,
		llm.Request{
			System: SystemPrompt(req.Settings,
				req.EvalName,
				req.Description,
				n),
			Messages: []llm.Message{{
				Role: llm.RoleUser,
				Content: UserPrompt(n,
					len(req.Keep)+have+1,
					queriesOf(req, st)),
			}},
			Model:     req.Settings.Model,
			MaxTokens: maxTokens(n),
		})
	if err != nil {
		return st, fmt.Errorf("the LLM call failed: %w", err)
	}
	next := withUsage(st, resp)
	cases, parseErr := ParseCases(resp.Text)
	if parseErr != nil {
		next.Rejected += n
		return report(ctx,
			lm,
			req,
			names,
			progress,
			next)
	}
	valid := validOnes(req,
		cases[:min(len(cases), n)],
		names[have:])
	next.Rejected += min(len(cases), n) - len(valid)
	next.Cases = append(append([]core.KnowledgeCase{}, next.Cases...), valid...)
	return report(ctx,
		lm,
		req,
		names,
		progress,
		next)
}

// report passes the state to progress and goes on.
func report(ctx context.Context,
	lm memory_core.LLM,
	req Request,
	names []string,
	progress func(Result),
	st Result) (Result, error) {
	if progress != nil {
		progress(st)
	}
	return step(ctx,
		lm,
		req,
		names,
		progress,
		st)
}

// withUsage is st with one more call that cost what resp says.
func withUsage(st Result,
	resp *llm.Response) Result {
	st.Calls++
	st.InputTokens += resp.InputTokens
	st.OutputTokens += resp.OutputTokens
	st.CostUSD += resp.CostUSD
	return st
}

// queriesOf are the queries of the cases that exist, kept and generated.
func queriesOf(req Request,
	st Result) []string {
	var out []string
	for _, c := range append(append([]core.KnowledgeCase{}, req.Keep...), st.Cases...) {
		if c.Query != "" {
			out = append(out, c.Query)
		}
	}
	return out
}

// validOnes names the cases (with names in order) and keeps the ones that pass the validation of a user eval.
func validOnes(req Request,
	cases []core.KnowledgeCase,
	names []string) []core.KnowledgeCase {
	out := []core.KnowledgeCase{}
	for _, c := range cases {
		c.Name = names[len(out)]
		if isValid(req, c) {
			out = append(out, c)
		}
	}
	return out
}

// isValid says whether the case passes ValidateUser as the only case of the eval.
func isValid(req Request,
	c core.KnowledgeCase) bool {
	doc, err := json.Marshal(map[string]any{
		"name":  req.EvalName,
		"cases": []core.KnowledgeCase{c},
	})
	if err != nil {
		return false
	}
	return len(evalfile.ValidateUser(doc,
		req.EvalName,
		req.Limits)) == 0
}
