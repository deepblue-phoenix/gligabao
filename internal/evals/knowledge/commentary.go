package knowledge

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/evals/ansi"
	"github.com/deepblue-phoenix/gligabao/internal/evals/commentary"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// DefaultCommentaryModel is the model that writes the commentary on a failed case (the -commentary-model flag of the runner).
const DefaultCommentaryModel = commentary.DefaultModel

// Commentary is the interactive eval commentary, a debugging aid for a person who runs the evals at a terminal (the prompt and the
// call are in package commentary). When a case fails (a check did not hold, or the case errored), the person is asked whether to run
// a commentary; if yes, the failure and the ATIF trajectories of the case are sent to an LLM, which says what the cause of the
// failure probably is. The commentary is printed under the failure log and the person has to confirm (Enter) before the run goes
// on, so there is time to read it.
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

// Offer asks the person whether to run a commentary on f and, if so, runs it, prints it on out under the failure log and waits for the
// person to confirm. It returns when that is done, or when ctx ends. Nothing it does (a refused prompt, a failed LLM call, input that
// ends) fails the case; a failed LLM call is printed instead of the commentary.
func (c *Commentary) Offer(ctx context.Context,
	out io.Writer,
	colors ansi.Palette,
	f commentary.Failure) {
	yes, err := c.askYesNo(ctx,
		out,
		colors.Yellow("Run commentary on this failure?")+" [y/N] ")
	if err != nil || !yes {
		return
	}
	model := c.Model
	if model == "" {
		model = DefaultCommentaryModel
	}
	fmt.Fprintf(out,
		"  %s\n",
		colors.Dim(fmt.Sprintf("asking %s ...", model)))
	text, resp, err := commentary.Run(ctx,
		c.LLM,
		model,
		c.MaxTokens,
		f)
	if err != nil {
		fmt.Fprintln(out, colors.Red(fmt.Sprintf("  commentary failed: %v", err)))
	} else {
		c.count++
		c.costUSD += resp.CostUSD
		cost := "cost unknown (no price for this model)"
		if resp.CostUSD > 0 {
			cost = fmt.Sprintf("cost $%.6f", resp.CostUSD)
		}
		fmt.Fprintf(out,
			"  %s\n%s\n  %s\n  %s\n",
			colors.Yellow(fmt.Sprintf("── commentary on %s (%s) ──",
				f.Case,
				resp.Model)),
			indentBlock(strings.TrimSpace(text), "  "),
			colors.Yellow("──"),
			colors.Dim(fmt.Sprintf("tokens: %d in, %d out; %s; commentary total this run: $%.6f (%d)",
				resp.InputTokens,
				resp.OutputTokens,
				cost,
				c.costUSD,
				c.count)))
	}
	if ctx.Err() != nil {
		return
	}
	fmt.Fprint(out, colors.Dim("Press Enter to continue ... "))
	_, _ = c.readLine(ctx) // an error here (input ended, ctx done) just means: go on
	fmt.Fprintln(out)
}

// indentBlock puts prefix before every line of s.
func indentBlock(s,
	prefix string) string {
	return prefix + strings.ReplaceAll(s,
		"\n",
		"\n"+prefix)
}

// askYesNo prints prompt and reads an answer until it is a yes or a no; an empty answer is a no. It returns an error if the input ended
// or ctx is done.
func (c *Commentary) askYesNo(ctx context.Context,
	out io.Writer,
	prompt string) (bool, error) {
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
