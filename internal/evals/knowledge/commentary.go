package knowledge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/evals/ansi"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// DefaultCommentaryModel is the model that writes the commentary on a failed case (the -commentary-model flag of the runner).
const DefaultCommentaryModel = "claude-sonnet-5-5"

// Commentary is the eval commentary: a debugging aid for a person who runs the evals at a terminal. When a case fails (a check did
// not hold, or the case errored), the person is asked whether to run a commentary; if yes, the failure and the ATIF trajectories of
// the case are sent to an LLM, which says what the cause of the failure probably is. The commentary is printed under the failure
// log and the person has to confirm (Enter) before the run goes on, so there is time to read it.
//
// It is meant for interactive runs only: it blocks on In. An Executor with a nil Commentary never asks anything.
type Commentary struct {
	// LLM writes the commentary.
	LLM memory_core.LLM
	// Model is the model that writes it (default DefaultCommentaryModel).
	Model string
	// In is where the answers of the person are read from (usually os.Stdin).
	In io.Reader
	// MaxTokens is the size of the commentary (default 2048).
	MaxTokens int

	in *bufio.Reader
	// What the commentaries of this run have cost so far.
	count   int
	costUSD float64
}

// Total returns how many commentaries were made and what they cost together, in USD (0 for a model without a known price).
func (c *Commentary) Total() (count int, costUSD float64) { return c.count, c.costUSD }

// commentaryBudget is the most bytes of trajectories (JSON) that are sent to the LLM; longer strings in them are cut to fit.
const commentaryBudget = 400_000

// commentaryFailure is what the commentary is about: one case that failed.
type commentaryFailure struct {
	Case         string
	Def          core.KnowledgeCaseDefMeta
	Input        core.KnowledgeInput // the input as it was run (the text files read in)
	RunModels    map[string]string   // the -models of the run
	RunReasoning map[string]string   // the -reasoning of the run
	Problems     []string
	Passed       []string
	Answer       string
	Err          error
	Trajectories []*atif.Trajectory
}

const commentarySystem = `You are a debugging assistant for "gligabao", a temporal knowledge-graph memory system. You are shown one failed end-to-end eval case.

How the system works:
- "remember" turns a text into a graph of entities and edges (facts with valid_at/invalid_at dates). Its pipeline records steps: extract_graph (LLM extracts entities and edges), find_candidates_by_name / search_similar_entities / load_candidates (stored facts that might duplicate or contradict the new ones), resolve_edges (LLM decides duplicates and contradictions; ended facts get invalid_at), embed, store_artifact, and optional geo (select_geo_entities, detect_geo, resolve_geo, store_geo), web_search and chunking (summarize_chunk) steps.
- "query" embeds the question (search_vectors), retrieves a subgraph and passages (retrieve_graph), then an LLM answers from that context.
- An eval case remembers some texts in order, then asks a question, and checks what was stored (expect_edges, expect_min_chunks, expect_web_searches, expect_series, expect_geo) and what the answer contains (expect_contains, case-insensitive).
- Each ATIF trajectory is one remember call or the query call: ordered steps (system/user/agent messages, tool calls with observations, per-step LLM metrics). Very long strings may have been cut ("...[N bytes cut]").

Your job: say what most likely caused the failure. Read the trajectories and find the first step where the behaviour diverges from what the case expects (for example: the extractor missed or mis-named a fact or date, the resolver closed or kept the wrong fact, retrieval did not surface the fact, the answering LLM ignored or misread its context, a lookup or provider call errored, or the case's expectation itself is wrong or too strict). Quote the evidence (step ids, tool names, the relevant values). Do not invent steps that are not in the trajectories; if the evidence is inconclusive, say so and say what to look at next.

Answer in plain text, no more than about 400 words, in this shape:
Cause: one or two sentences.
Where: the stage and step id(s).
Evidence: short bullet points.
Suggestion: what to change (prompt, model, case expectation, code) or check; several possibilities may be listed.
Final advice: exactly one piece of advice, the one with the highest probability of resolving this failure. If the case definition itself is the problem (its input text, expected values or params need to change, or it is invalid or contradictory, for example an expectation that the input does not support or that contradicts another expectation or the params), say so and give the concrete change (the new input, expected value or param). Otherwise give the single most likely fix for the cause (prompt, model, code or setting), as concrete as you can. Do not repeat the list of the Suggestion; pick one.`

// Offer asks the person whether to run a commentary on f and, if so, runs it, prints it on out under the failure log and waits for the
// person to confirm. It returns when that is done, or when ctx ends. Nothing it does (a refused prompt, a failed LLM call, input that
// ends) fails the case; a failed LLM call is printed instead of the commentary.
func (c *Commentary) Offer(ctx context.Context, out io.Writer, colors ansi.Palette, f commentaryFailure) {
	yes, err := c.askYesNo(ctx, out, colors.Yellow("Run commentary on this failure?")+" [y/N] ")
	if err != nil || !yes {
		return
	}
	model := c.Model
	if model == "" {
		model = DefaultCommentaryModel
	}
	fmt.Fprintf(out, "  %s\n", colors.Dim(fmt.Sprintf("asking %s ...", model)))
	text, resp, err := c.run(ctx, model, f)
	if err != nil {
		fmt.Fprintln(out, colors.Red(fmt.Sprintf("  commentary failed: %v", err)))
	} else {
		c.count++
		c.costUSD += resp.CostUSD
		cost := "cost unknown (no price for this model)"
		if resp.CostUSD > 0 {
			cost = fmt.Sprintf("cost $%.6f", resp.CostUSD)
		}
		fmt.Fprintf(out, "  %s\n%s\n  %s\n  %s\n", colors.Yellow(fmt.Sprintf("── commentary on %s (%s) ──", f.Case, resp.Model)),
			indentBlock(strings.TrimSpace(text), "  "), colors.Yellow("──"),
			colors.Dim(fmt.Sprintf("tokens: %d in, %d out; %s; commentary total this run: $%.6f (%d)", resp.InputTokens, resp.OutputTokens, cost, c.costUSD, c.count)))
	}
	if ctx.Err() != nil {
		return
	}
	fmt.Fprint(out, colors.Dim("Press Enter to continue ... "))
	_, _ = c.readLine(ctx) // an error here (input ended, ctx done) just means: go on
	fmt.Fprintln(out)
}

// run sends the failure to the LLM and returns the commentary.
func (c *Commentary) run(ctx context.Context, model string, f commentaryFailure) (string, *ai.Response, error) {
	if c.LLM == nil {
		return "", nil, errors.New("no LLM")
	}
	max := c.MaxTokens
	if max <= 0 {
		max = 2048
	}
	resp, err := c.LLM.Complete(ctx, ai.Request{
		System:    commentarySystem,
		Messages:  []ai.Message{{Role: ai.RoleUser, Content: commentaryPrompt(f)}},
		Model:     model,
		MaxTokens: max,
	})
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(resp.Text) == "" {
		return "", nil, errors.New("the model returned no text")
	}
	return resp.Text, resp, nil
}

// commentaryPrompt is the message sent to the LLM: the case, what failed, and the trajectories.
func commentaryPrompt(f commentaryFailure) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Eval case: %s\n\n", f.Case)

	// The case: its input (as it ran) and what it expects, as JSON; long texts are cut (the trajectories hold them as well).
	caseInfo := map[string]any{
		"input":    f.Input,
		"expected": f.Def.KnowledgeExpected,
		"params":   f.Def.KnowledgeParams,
	}
	if len(f.RunModels) > 0 {
		caseInfo["run_models"] = f.RunModels
	}
	if len(f.RunReasoning) > 0 {
		caseInfo["run_reasoning"] = f.RunReasoning
	}
	b.WriteString("Case definition (JSON):\n")
	b.WriteString(compactJSON(caseInfo, 6000))
	b.WriteString("\n\nFailure:\n")
	if f.Err != nil {
		fmt.Fprintf(&b, "- the case could not run to the end: %v\n", f.Err)
	}
	for _, p := range f.Problems {
		fmt.Fprintf(&b, "- failed check: %s\n", p)
	}
	if f.Answer != "" {
		fmt.Fprintf(&b, "- answer given by the query: %q\n", f.Answer)
	}
	if len(f.Passed) > 0 {
		b.WriteString("\nChecks that held:\n")
		for _, p := range f.Passed {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}

	trajs := trajectoriesJSON(f.Trajectories, commentaryBudget)
	if len(trajs) == 0 {
		b.WriteString("\nNo trajectory was recorded for this case (it failed before any remember or query call returned).\n")
	}
	for i, t := range trajs {
		fmt.Fprintf(&b, "\nATIF trajectory %d of %d (%s):\n%s\n", i+1, len(trajs), f.Trajectories[i].Agent.Name, t)
	}
	return b.String()
}

// trajectoriesJSON encodes the trajectories as compact JSON, cutting the longest strings in them (progressively harder) until the
// total is at most budget bytes, or the strings are cut to a stub.
func trajectoriesJSON(ts []*atif.Trajectory, budget int) []string {
	if len(ts) == 0 {
		return nil
	}
	var generic []any
	for _, t := range ts {
		raw, err := json.Marshal(t)
		if err != nil {
			generic = append(generic, fmt.Sprintf("(trajectory could not be encoded: %v)", err))
			continue
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		generic = append(generic, v)
	}
	limits := []int{0, 20000, 5000, 1500, 500, 150}
	for i, limit := range limits {
		out, total := encodeAll(generic, limit)
		if total <= budget || i == len(limits)-1 {
			return out
		}
	}
	return nil // not reached
}

func encodeAll(generic []any, limit int) (out []string, total int) {
	for _, v := range generic {
		s := compactJSON(v, limit)
		out = append(out, s)
		total += len(s)
	}
	return out, total
}

// compactJSON encodes v as one line of JSON, with every string longer than limit bytes cut (limit 0: none are).
func compactJSON(v any, limit int) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("(could not be encoded: %v)", err)
	}
	if limit <= 0 {
		return string(raw)
	}
	var generic any
	if json.Unmarshal(raw, &generic) != nil {
		return string(raw)
	}
	raw, err = json.Marshal(cutStrings(generic, limit))
	if err != nil {
		return string(raw)
	}
	return string(raw)
}

// cutStrings returns v (decoded JSON) with each string longer than limit bytes cut and marked.
func cutStrings(v any, limit int) any {
	switch x := v.(type) {
	case string:
		if len(x) > limit {
			cut := limit
			for cut > 0 && !isRuneStart(x[cut]) {
				cut--
			}
			return fmt.Sprintf("%s...[%d bytes cut]", x[:cut], len(x)-cut)
		}
		return x
	case []any:
		for i := range x {
			x[i] = cutStrings(x[i], limit)
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = cutStrings(x[k], limit)
		}
		return x
	}
	return v
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// indentBlock puts prefix before every line of s.
func indentBlock(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// askYesNo prints prompt and reads an answer until it is a yes or a no; an empty answer is a no. It returns an error if the input ended
// or ctx is done.
func (c *Commentary) askYesNo(ctx context.Context, out io.Writer, prompt string) (bool, error) {
	for {
		fmt.Fprint(out, prompt)
		line, err := c.readLine(ctx)
		if err != nil {
			fmt.Fprintln(out)
			return false, err
		}
		switch strings.ToLower(line) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		}
		fmt.Fprintln(out, "  please answer y or n")
	}
}

// readLine reads a line of In (without the line end), or returns when ctx is done (the read itself cannot be interrupted: it is left
// to finish in the background, which only matters for a run that is being cancelled anyway).
func (c *Commentary) readLine(ctx context.Context) (string, error) {
	if c.in == nil {
		if c.In == nil {
			return "", io.EOF
		}
		c.in = bufio.NewReader(c.In)
	}
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := c.in.ReadString('\n')
		ch <- result{s, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil && r.s == "" {
			return "", r.err
		}
		return strings.TrimSpace(r.s), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
