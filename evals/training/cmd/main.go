// Command training runs the evals of a trained model (evals/training/evals/*.json) from the command line: the model is asked
// each case's question through the sidecar, and the LLM judge (the one used for knowledge ingestion) scores its answer
// against the case's expected answer, as the Eval button of the admin UI does (see ../README.md).
//
// Usage: go run ./evals/training/cmd -model ID [-v] [-eval name] [case-name ...]   (requires ANTHROPIC_API_KEY and a running sidecar)
// -list-evals prints the available evals and exits; -list-models prints the trained models of the database and exits.
// The results are printed, not stored. The exit status is 1 if any case is not correct (score below 4) or failed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	trainingeval "github.com/deepblue-phoenix/gligabao/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
)

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

func mainErr() error {
	verbose := flag.Bool("v", false, "print the question, the expected answer, the model's answer and the judge's reason of every case")
	eval := flag.String("eval", "", "run only the cases of this eval (file name without .json; see -list-evals)")
	listEvals := flag.Bool("list-evals", false, "print the available evals and their cases, then exit")
	listModels := flag.Bool("list-models", false, "print the trained models of the database (the ids -model takes), then exit")
	modelID := flag.Int64("model", 0, "id of the trained model to evaluate (see -list-models)")
	dbPath := flag.String("db", "bin/gligabao.db", "SQLite file of the database that holds the trained models")
	sidecarURL := flag.String("sidecar", os.Getenv(config.EnvSidecarURL), "URL of the sidecar (default: $SIDECAR_URL, else "+sidecar.DefaultBaseURL+")")
	batchSize := flag.Int("batch-size", 0, fmt.Sprintf("question/answer pairs per judge call (default %d, at most %d)", trainingeval.DefaultBatchSize, trainingeval.MaxBatchSize))
	judgeModel := flag.String("judge-model", "", "the judge's LLM model (default: the LLM's default)")
	maxTokens := flag.Int("max-tokens", 1024, "max tokens of the model's answers")
	temperature := flag.Float64("temperature", 0.1, "temperature of the model's answers")
	flag.Parse()

	if *listEvals {
		return printEvals(os.Stdout)
	}
	cases, err := trainingeval.LoadEvals(flag.Args(), *eval)
	if err != nil {
		return err
	}
	cfg, err := trainingeval.Config{BatchSize: *batchSize, JudgeModel: *judgeModel}.Normalize()
	if err != nil {
		return err
	}
	if *maxTokens < 1 || *temperature <= 0 || *temperature > 2 {
		return errors.New("-max-tokens must be at least 1 and -temperature above 0 and at most 2")
	}

	database, err := db.Open(db.Config{SQLitePath: *dbPath})
	if err != nil {
		return err
	}
	defer database.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *listModels {
		models, err := relational.ListTrainedModels(ctx, database.Relational, 0)
		if err != nil {
			return err
		}
		for _, m := range models {
			fmt.Printf("  %4d  %s  (%s, %s)\n", m.ID, m.Name, m.ModelSize, m.Dataset)
		}
		if len(models) == 0 {
			fmt.Println("  no trained models in", *dbPath)
		}
		return nil
	}
	if *modelID < 1 {
		return errors.New("-model must be the id of a trained model (see -list-models)")
	}
	tm, err := relational.GetTrainedModel(ctx, database.Relational, *modelID)
	if err != nil {
		return fmt.Errorf("trained model %d: %w", *modelID, err)
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return errors.New("ANTHROPIC_API_KEY must be set to run evals")
	}
	judge, err := ai.New(key)
	if err != nil {
		return err
	}
	sc := sidecar.New(*sidecarURL)
	if _, err := sc.Health(ctx); err != nil {
		return err
	}

	gen := func(ctx context.Context, prompt string) (trainingeval.Generation, error) {
		out, err := sc.Infer(ctx, sidecar.InferRequest{Prompt: prompt, ModelSize: tm.ModelSize, ModelPath: tm.Path,
			MaxTokens: *maxTokens, Temperature: temperature})
		if err != nil {
			var ie *sidecar.InferError
			if errors.As(err, &ie) {
				return trainingeval.Generation{}, errors.New(ie.Message)
			}
			return trainingeval.Generation{}, err
		}
		return trainingeval.Generation{Response: out.Response, InputTokens: out.InputTokens, OutputTokens: out.OutputTokens}, nil
	}
	examples := make([]trainingeval.Example, len(cases))
	for i, c := range cases {
		examples[i] = c.Example()
	}
	fmt.Printf("model %d %q (%s): %d cases\n", tm.ID, tm.Name, tm.ModelSize, len(cases))

	failed := 0
	sink := func(_ context.Context, results []trainingeval.CaseResult, _ trainingeval.Progress) error {
		for _, r := range results {
			c := cases[r.Idx]
			verdict := "FAIL"
			detail := r.Error
			if r.Score != nil {
				verdict = strings.ToUpper(trainingeval.VerdictOf(*r.Score))
				detail = fmt.Sprintf("score %d: %s", *r.Score, r.Reason)
			}
			if r.Score == nil || trainingeval.VerdictOf(*r.Score) != trainingeval.VerdictCorrect {
				failed++
			}
			fmt.Printf("%-9s %s/%s  %s\n", verdict, c.Eval, c.Name, detail)
			if *verbose {
				fmt.Printf("    question:  %s\n    expected:  %s\n    answer:    %s\n", r.Instruction, r.Reference, r.Response)
			}
		}
		return nil
	}
	p, err := trainingeval.Run(ctx, cfg, examples, gen, judge, sink)
	if err != nil {
		return err
	}
	fmt.Printf("---\ncorrect: %d, partial: %d, incorrect: %d, failed: %d; mean score %.2f / %d; judge cost $%.6f\n",
		p.Correct, p.Partial, p.Incorrect, p.Failed, p.MeanScore(), trainingeval.MaxScore, p.JudgeCostUSD)
	if failed > 0 {
		os.Exit(1)
	}
	return nil
}

// printEvals prints the evals with their cases, as a table.
func printEvals(w *os.File) error {
	cases, err := trainingeval.LoadEvals(nil, "")
	if err != nil {
		return err
	}
	for _, n := range trainingeval.EvalNames() {
		var in []string
		for _, c := range cases {
			if c.Eval == n {
				in = append(in, c.Name)
			}
		}
		fmt.Fprintf(w, "  %s  %d cases  %s\n", n, len(in), strings.Join(in, ", "))
	}
	return nil
}
