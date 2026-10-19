// Command training runs the evals of a trained model (bin/evals_training/*.json) from the command line: the model is asked
// each case's question through the sidecar, and the LLM judge (the one used for knowledge ingestion) scores its answer
// against the case's expected answer. It is the Eval button of the admin UI (POST /v1/gb/training/models/{id}/evals) without the
// server: the same eval, the same questions to the model, the same judge, and the run is stored in the database the same way
// (an eval run with its cases, which the admin UI shows), unless -no-store is given (see ../README.md).
//
// Usage: go run ./internal/evals/training/cmd -model ID [-v] [-eval name] [case-name ...]   (requires ANTHROPIC_API_KEY and a running sidecar)
// Without -eval the eval is the one named like the model's training dataset, as for the button.
// -list-evals prints the available evals and exits; -list-models prints the trained models of the database and exits.
// The results are printed and stored. The exit status is 1 if any case is not correct (score below 4) or errored.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/config"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	trainingeval "github.com/deepblue-phoenix/gligabao/internal/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sidecar"
	"github.com/deepblue-phoenix/gligabao/internal/training"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
)

// defaultModelSize is the size of the model evaluated when -model is not given (Gemma 4 4B, google/gemma-4-E4B-it).
const defaultModelSize = "4b"

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

func mainErr() error {
	verbose := flag.Bool("v", false, "print the question, the expected answer, the model's answer and the judge's reason of every case")
	eval := flag.String("eval", "", "the eval to run (file name without .json; see -list-evals; default: the eval named like the model's training dataset)")
	evalsDir := flag.String("evals", trainingeval.DefaultEvalDir, "directory of the evals (*.json files)")
	noStore := flag.Bool("no-store", false, "do not store the run in the database")
	listEvals := flag.Bool("list-evals", false, "print the available evals and their cases, then exit")
	listModels := flag.Bool("list-models", false, "print the trained models of the database (the ids -model takes), then exit")
	modelID := flag.Int64("model", 0, "id of the trained model to evaluate (see -list-models; default: the newest Gemma 4 4B one)")
	dbPath := flag.String("db", "bin/gligabao.db", "SQLite file of the database that holds the trained models")
	sidecarURL := flag.String("sidecar", os.Getenv(config.EnvSidecarURL), "URL of the sidecar (default: $SIDECAR_URL, else "+sidecar.DefaultBaseURL+")")
	batchSize := flag.Int("batch-size", 0, fmt.Sprintf("question/answer pairs per judge call (default %d, at most %d)", trainingeval.DefaultBatchSize, trainingeval.MaxBatchSize))
	judgeModel := flag.String("judge-model", "", "the judge's LLM model (default: the LLM's default)")
	maxTokens := flag.Int("max-tokens", 1024, "max tokens of the model's answers")
	temperature := flag.Float64("temperature", 0.1, "temperature of the model's answers")
	flag.Parse()

	if *listEvals {
		return printEvals(os.Stdout, *evalsDir)
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
		models, err := trainingdb.ListTrainedModels(ctx, database.Relational, 0)
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
	if *modelID < 0 {
		return errors.New("-model must be the id of a trained model (see -list-models)")
	}
	if *modelID == 0 {
		// The default model: the newest trained Gemma 4 4B (the newest are listed first).
		models, err := trainingdb.ListTrainedModels(ctx, database.Relational, 0)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(models, func(m trainingdb.TrainedModel) bool { return m.ModelSize == defaultModelSize })
		if i < 0 {
			return fmt.Errorf("no trained %s model in %s: train one, or pass -model ID (see -list-models)", defaultModelSize, *dbPath)
		}
		*modelID = models[i].ID
	}
	tm, err := trainingdb.GetTrainedModel(ctx, database.Relational, *modelID)
	if err != nil {
		return fmt.Errorf("trained model %d: %w", *modelID, err)
	}
	name := *eval
	if name == "" {
		if name = strings.TrimSuffix(tm.Dataset, ".jsonl"); name == "" || !slices.Contains(trainingeval.EvalNames(*evalsDir), name) {
			return fmt.Errorf("no eval is named like the training dataset of model %d: use -eval (%s)", tm.ID, strings.Join(trainingeval.EvalNames(*evalsDir), ", "))
		}
	}
	cases, err := trainingeval.LoadEvals(*evalsDir, flag.Args(), name)
	if err != nil {
		return err
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return errors.New("ANTHROPIC_API_KEY must be set to run evals")
	}
	judgeLLM, err := ai.New(key)
	if err != nil {
		return err
	}
	sc := sidecar.New(*sidecarURL)
	if _, err := sc.Health(ctx); err != nil {
		return err
	}

	params := training.RunParams{BatchSize: cfg.BatchSize, MaxTokens: *maxTokens, Temperature: *temperature}
	if len(flag.Args()) > 0 {
		params.Cases = flag.Args()
	}
	gen := training.Generator(sc, tm, params)
	examples, names := make([]trainingeval.Example, len(cases)), make([]string, len(cases))
	for i, c := range cases {
		examples[i], names[i] = c.Example(), c.Name
	}
	fmt.Printf("model %d %q (%s): eval %s, %d cases\n", tm.ID, tm.Name, tm.ModelSize, name, len(cases))

	// The run is stored like the server's (the admin UI shows it); the judge's calls are recorded as the server's are.
	var runID int64
	if !*noStore {
		pj, _ := json.Marshal(params)
		if runID, err = evalsdb.RecordTrainedModelEvalRun(ctx, database.Relational, evalsdb.TrainedModelRun{
			TrainedModelID: tm.ID, Eval: name, Params: pj, JudgeModel: cfg.JudgeModel, NumExamples: len(cases)}); err != nil {
			return err
		}
		fmt.Printf("stored as eval run #%d\n", runID)
	}
	llm := memory_core.LLM(judgeLLM)
	if !*noStore {
		llm = memory_core.WithRecording(judgeLLM, database.Relational, training.CallEvalJudge)
	}

	failed := 0
	sink := func(_ context.Context, results []trainingeval.CaseResult, p trainingeval.Progress) error {
		if !*noStore {
			rows := make([]evalsdb.TrainedModelCase, len(results))
			for i, r := range results {
				rows[i] = training.CaseRow(runID, names, r)
			}
			if err := evalsdb.SaveTrainedModelEvalRunBatch(context.WithoutCancel(ctx), database.Relational, training.RunProgress(runID, p), rows); err != nil {
				return err
			}
		}
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
	p, err := trainingeval.Run(memory_core.WithStage(ctx, training.StageJudge), cfg, examples, gen, llm, sink)
	if !*noStore {
		status, errText := evalsdb.TrainedModelRunCompleted, ""
		switch {
		case errors.Is(err, context.Canceled):
			status = evalsdb.TrainedModelRunCancelled
		case err != nil:
			status, errText = evalsdb.TrainedModelRunErrored, err.Error()
		}
		if ferr := evalsdb.FinishTrainedModelEvalRun(context.WithoutCancel(ctx), database.Relational, runID, status, errText); ferr != nil {
			fmt.Fprintln(os.Stderr, "error: ending the stored run:", ferr)
		}
	}
	if err != nil {
		return err
	}
	fmt.Printf("---\ncorrect: %d, partial: %d, incorrect: %d, errored: %d; mean score %.2f / %d; judge cost $%.6f\n",
		p.Correct, p.Partial, p.Incorrect, p.Errored, p.MeanScore(), trainingeval.MaxScore, p.JudgeCostUSD)
	if failed > 0 {
		os.Exit(1)
	}
	return nil
}

// printEvals prints the evals with their cases, as a table.
func printEvals(w *os.File, dir string) error {
	cases, err := trainingeval.LoadEvals(dir, nil, "")
	if err != nil {
		return err
	}
	for _, n := range trainingeval.EvalNames(dir) {
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
