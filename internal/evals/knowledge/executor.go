package knowledge

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// Executor runs the cases of a knowledge eval (it implements runner.Executor). Start the eval run with a
// *core.KnowledgeEvalRunMeta as EvalOptions.Meta; the Executor keeps it up to date.
type Executor struct {
	// DB is the eval database: the datasets of the cases are made in it (it should also be the database the runner records in, so
	// the admin UI shows the graphs next to the runs). It is never reset.
	DB *db.DB
	// LLM is the model the knowledge extraction runs on, Embedder embeds the entities and edges into the vector store of DB (nil: they
	// are not vector indexed) and WebSearch runs the web searches of the remember entries that ask for them (nil: none).
	LLM       memory_core.LLM
	Embedder  memory_core.Embedder
	WebSearch memory_core.WebSearcher
	// EvalsDir is the directory the TextFiles of the cases are relative to (default DefaultEvalDir).
	EvalsDir string
	// Models is the default LLM model of each stage of remember; a case's own models win over it. Reasoning is the reasoning
	// level of each stage, and of the recall with the key "recall"; a case's own win over it (see ParseReasoning).
	Models    memory_core.Models
	Reasoning map[string]string
	// TrajectoryDir is where the server stored the ATIF trajectories (memory_core.TrajectoryDir); the case runs reference the files.
	TrajectoryDir string
	// Verbose prints the input and output of every LLM call and what was stored, to Out.
	Verbose bool
	// Out is where the progress goes: PASS and FAIL for each case (nil discards).
	Out io.Writer
}

func (x *Executor) out() io.Writer {
	if x.Out == nil {
		return io.Discard
	}
	return x.Out
}

// Execute implements runner.Executor: the cases of def.Cases one after the other, each recorded as a case run when it ends. A case
// that fails is errored (or cancelled, if ctx ended) and the next one runs.
func (x *Executor) Execute(ctx context.Context, rec *runner.EvalRecorder, def *core.EvalDef) error {
	meta, ok := rec.Run().Meta.(*core.KnowledgeEvalRunMeta)
	if !ok {
		return fmt.Errorf("a knowledge eval run needs a *core.KnowledgeEvalRunMeta, not %T", rec.Run().Meta)
	}
	dbCtx := context.WithoutCancel(ctx) // what is recorded must outlive a cancelled run
	for _, c := range def.Cases {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := x.executeCase(ctx, dbCtx, rec, meta, c); err != nil {
			return err
		}
	}
	return nil
}

func (x *Executor) executeCase(ctx, dbCtx context.Context, rec *runner.EvalRecorder, evalMeta *core.KnowledgeEvalRunMeta, c core.CaseDef) error {
	m, ok := c.Meta.(*core.KnowledgeCaseDefMeta)
	if !ok {
		return fmt.Errorf("case def %q has no knowledge metadata (%T)", c.Name, c.Meta)
	}
	cr, err := rec.StartCase(dbCtx, c)
	if err != nil {
		return err
	}
	llm := x.LLM
	if x.Verbose {
		fmt.Fprintf(x.out(), "=== %s\n", c.Name)
		llm = &verboseLLM{inner: x.LLM, out: x.out()}
	}

	out := &outcome{}
	input, err := readTexts(x.EvalsDir, m.KnowledgeInput)
	if err == nil {
		out, err = x.runCase(ctx, c.Name, *m, input, rec.Run().ID, llm)
	}
	cost, steps := out.cost()
	if x.Verbose {
		if out.Chunks > 0 {
			fmt.Fprintf(x.out(), "  chunks: %d\n", out.Chunks)
		}
		fmt.Fprintf(x.out(), "  trajectories: %d, steps: %d, cost: $%.6f\n", len(out.Trajectories), steps, cost)
	}

	// Everything that makes the case fail: failed checks, then expected answer text that is missing.
	problems, passed := out.Problems, out.PassedChecks
	if err == nil {
		for _, want := range m.ExpectContains {
			if strings.Contains(strings.ToLower(out.Answer), strings.ToLower(want)) {
				passed = append(passed, fmt.Sprintf("answer contains %q", want))
			} else {
				problems = append(problems, fmt.Sprintf("answer does not contain %q", want))
			}
		}
	}

	meta := &core.KnowledgeCaseRunMeta{
		Input: input, Answer: out.Answer, Problems: problems, PassedChecks: passed, Steps: steps,
	}
	if out.DatasetID != 0 {
		id := out.DatasetID
		meta.DatasetID = &id
	}
	for i, t := range out.Trajectories {
		var ids []int64
		if i < len(out.ArtifactIDs) {
			ids = out.ArtifactIDs[i]
		}
		ref, rerr := trajectoryRef(i, t, x.TrajectoryDir, ids)
		if rerr != nil {
			if err == nil {
				err = rerr
			}
			continue
		}
		meta.Trajectories = append(meta.Trajectories, ref)
		if ref.Kind == KindRemember {
			evalMeta.RememberCalls++
			evalMeta.RememberCostUSD += ref.CostUSD
		} else {
			evalMeta.RecallCalls++
			evalMeta.RecallCostUSD += ref.CostUSD
		}
	}
	evalMeta.Chunks += out.Chunks
	evalMeta.Steps += steps
	evalMeta.ChecksPassed += len(passed)
	evalMeta.ChecksFailed += len(problems)

	cr.CostUSD = cost
	switch {
	case err != nil && ctx.Err() != nil:
		cr.Status, cr.Error = core.StatusCancelled, err.Error()
	case err != nil:
		cr.Status, cr.Error = core.StatusErrored, err.Error()
	case len(problems) > 0:
		cr.Status, cr.Verdict = core.StatusCompleted, core.VerdictFail
	default:
		cr.Status, cr.Verdict = core.StatusCompleted, core.VerdictPass
	}
	if ferr := rec.FinishCase(dbCtx, cr, meta); ferr != nil {
		return ferr
	}

	switch {
	case err != nil:
		fmt.Fprintf(x.out(), "FAIL %s: %v\n", c.Name, err)
	case len(problems) > 0:
		fmt.Fprintf(x.out(), "FAIL %s:\n     %s\n     answer: %s\n", c.Name, strings.Join(problems, "\n     "), out.Answer)
	default:
		fmt.Fprintf(x.out(), "PASS %s: %s\n", c.Name, out.Answer)
	}
	return nil
}
