// Command training runs the training evals (build/evals_training/*.json) against a trained model from the command line: the model is
// asked each case's question through the sidecar, and the LLM judge (the one used for knowledge ingestion) scores its answer against
// the case's expected answer. The eval is put in the database as an eval def (its cases as case defs), and the run is stored as an eval
// run with a case run for each case.
//
// Usage: go run ./internal/evals/training/cmd -model ID [-v] [-config file] [-evals dir ...] [-eval name] [case-name ...]   (requires ANTHROPIC_API_KEY and a running sidecar)
// Without -eval the eval is the one named like the model's training dataset.
// -list-evals prints the available evals and exits; -list-models prints the trained models of the database and exits.
// Every invocation is a run of an experiment: -experiment names it (created if new; -experiment "" runs the eval on its own), and without
// the flag it is named with the start time, so it is unique (see runner.ExperimentName).
// The exit status is 1 if any case is not correct (score below core.TrainingPassScore) or errored.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals"
	"github.com/deepblue-phoenix/gligabao/internal/evals/ansi"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
	"github.com/deepblue-phoenix/gligabao/internal/evals/training"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/sys/sidecar"
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
	eval := flag.String("eval", "", "the eval to run (see -list-evals; default: the eval named like the model's training dataset)")
	var evalsFlag evalfile.DirsFlag
	flag.Var(&evalsFlag, "evals", "directory of the evals (*.json files); repeatable, searched in order (default: evals_training_dirs of the config, else "+training.DefaultEvalDir+")")
	configPath := flag.String("config", evalfile.DefaultConfigPath, "config file whose evals_training_dirs lists the eval directories (relative paths in it are relative to the file)")
	listEvals := flag.Bool("list-evals", false, "print the available evals and their cases, then exit")
	listModels := flag.Bool("list-models", false, "print the trained models of the database (the ids -model takes), then exit")
	modelID := flag.Int64("model", 0, "id of the trained model to evaluate (see -list-models; default: the newest Gemma 4 4B one)")
	dbPath := flag.String("db", "build/gligabao.db", "SQLite file of the database that holds the trained models (and the evals and their runs)")
	sidecarURL := flag.String("sidecar", os.Getenv(config.EnvSidecarURL), "URL of the sidecar (default: $SIDECAR_URL, else "+sidecar.DefaultBaseURL+")")
	batchSize := flag.Int("batch-size", 0, fmt.Sprintf("question/answer pairs per judge call (default %d, at most %d)", training.DefaultBatchSize, training.MaxBatchSize))
	judgeModel := flag.String("judge-model", "", "the judge's LLM model (default: the LLM's default)")
	maxTokens := flag.Int("max-tokens", 1024, "max tokens of the model's answers")
	temperature := flag.Float64("temperature", 0.1, "temperature of the model's answers")
	experiment := flag.String("experiment", "", `name of the experiment this run belongs to (created if new); "" runs the eval on its own; if not given: the start time`)
	experimentDesc := flag.String("experiment-desc", "", "description of the experiment, used when it is created")
	flag.Parse()

	configSet := false
	flag.Visit(func(f *flag.Flag) { configSet = configSet || f.Name == "config" })
	evalsDirs, err := evalfile.ResolveDirs(evalsFlag, *configPath, configSet, func(c *config.Config) []string { return c.EvalsTrainingDirs }, training.DefaultEvalDir)
	if err != nil {
		return err
	}
	loaded, err := training.LoadEvals(evalsDirs...)
	if err != nil {
		return err
	}
	if *listEvals {
		printEvals(os.Stdout, loaded)
		return nil
	}
	cfg, err := training.Config{BatchSize: *batchSize, JudgeModel: *judgeModel}.Normalize()
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
		if name = strings.TrimSuffix(tm.Dataset, ".jsonl"); name == "" || !slices.Contains(trainingNames(loaded), name) {
			return fmt.Errorf("no eval is named like the training dataset of model %d: use -eval (%s)", tm.ID, strings.Join(trainingNames(loaded), ", "))
		}
	}
	selected, err := training.SelectEvals(loaded, name, flag.Args())
	if err != nil {
		return err
	}
	if len(selected) != 1 {
		return fmt.Errorf("eval %q has no cases", name)
	}
	ev := selected[0]

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

	store, err := evals.StoreFor(database.Backend)
	if err != nil {
		return err
	}
	r := runner.New(database.Relational, store)
	def, err := r.SyncEval(ctx, training.Spec(ev))
	if err != nil {
		return err
	}
	colors := ansi.New(os.Stdout)
	fmt.Printf("model %d %q (%s): eval %s, %d cases\n", tm.ID, tm.Name, tm.ModelSize, colors.Yellow(name), len(def.Cases))

	failed := 0
	x := &training.Executor{
		Config:   cfg,
		Generate: generator(sc, tm, *maxTokens, *temperature),
		Judge:    memory_core.WithRecording(judgeLLM, database.Relational, training.CallJudge),
		OnBatch: func(results []training.CaseResult, _ training.Progress) {
			for _, res := range results {
				c := def.Cases[res.Idx]
				verdict, detail := "FAIL", res.Error
				if res.Score != nil {
					verdict = strings.ToUpper(string(training.BandOf(*res.Score)))
					detail = fmt.Sprintf("score %d: %s", *res.Score, res.Reason)
				}
				ok := res.Score != nil && *res.Score >= core.TrainingPassScore
				if !ok {
					failed++
				}
				fmt.Println(colors.Verdict(ok, fmt.Sprintf("%-9s %s/%s  %s", verdict, colors.Yellow(name), c.Name, detail)))
				if *verbose {
					fmt.Println(colors.Verdict(ok, fmt.Sprintf("    question:  %s\n    expected:  %s\n    %s    %s",
						res.Instruction, res.Reference, colors.Yellow("answer:"), res.Response)))
				}
			}
		},
	}
	params := core.Params{"evals_dirs": evalsDirs, "batch_size": cfg.BatchSize, "max_tokens": *maxTokens, "temperature": *temperature}
	if cfg.JudgeModel != "" {
		params["judge_model"] = cfg.JudgeModel
	}
	if len(flag.Args()) > 0 {
		params["cases"] = flag.Args()
	}
	// The run is a run of an experiment: the one named by -experiment, else one named with the start time (unique).
	experimentSet := false
	flag.Visit(func(f *flag.Flag) { experimentSet = experimentSet || f.Name == "experiment" })
	*experiment = runner.ExperimentName(experimentSet, *experiment, "", time.Now())
	opts := runner.EvalOptions{Params: params, Meta: &core.TrainingEvalRunMeta{TrainedModelID: tm.ID}}
	var xr *runner.ExperimentRecorder
	if *experiment != "" {
		xdef, err := r.EnsureExperiment(ctx, *experiment, *experimentDesc, []core.EvalDefID{def.ID})
		if err != nil {
			return err
		}
		if xr, err = r.StartExperimentRun(ctx, xdef, params); err != nil {
			return fmt.Errorf("start experiment: %w", err)
		}
		opts.ExperimentRunID = &xr.Run().ID
		fmt.Printf("experiment %s\n", colors.Yellow(xdef.Name))
	}
	run, err := r.RunEval(memory_core.WithStage(ctx, training.StageJudge), def, x, opts)
	if xr != nil {
		if run != nil {
			if addErr := xr.Add(context.WithoutCancel(ctx), run); addErr != nil {
				fmt.Fprintf(os.Stderr, "warning: record the eval in experiment %q: %v\n", *experiment, addErr)
			}
		}
		if finishErr := xr.Finish(ctx, err); finishErr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", finishErr)
		}
	}
	if run != nil {
		fmt.Printf("stored as eval run #%d (%s)\n", run.ID, run.Status)
	}
	if err != nil {
		return err
	}
	m := run.Meta.(*core.TrainingEvalRunMeta)
	mean := 0.0
	if m.NumJudged > 0 {
		mean = float64(m.ScoreSum) / float64(m.NumJudged)
	}
	fmt.Printf("---\n%s, partial: %d, %s, %s; mean score %.2f / %d; judge cost $%.6f\n",
		colors.Green(fmt.Sprintf("correct: %d", m.NumCorrect)), m.NumPartial,
		colors.Verdict(m.NumIncorrect == 0, fmt.Sprintf("incorrect: %d", m.NumIncorrect)),
		colors.Verdict(run.Counts.Errored == 0, fmt.Sprintf("errored: %d", run.Counts.Errored)),
		mean, training.MaxScore, m.JudgeCostUSD)
	if failed > 0 {
		os.Exit(1)
	}
	return nil
}

// inferTimeout bounds one inference call: the model loads on a GPU container first, which takes minutes when cold.
const inferTimeout = 15 * time.Minute

// generator makes the engine's GenerateFunc of trained model tm: one sidecar inference per question.
func generator(sc *sidecar.Client, tm *trainingdb.TrainedModel, maxTokens int, temperature float64) training.GenerateFunc {
	return func(ctx context.Context, prompt string) (training.Generation, error) {
		ctx, cancel := context.WithTimeout(ctx, inferTimeout)
		defer cancel()
		out, err := sc.Infer(ctx, sidecar.InferRequest{Prompt: prompt, ModelSize: tm.ModelSize, ModelPath: tm.Path, MaxTokens: maxTokens, Temperature: &temperature})
		if err != nil {
			var ie *sidecar.InferError
			if errors.As(err, &ie) {
				return training.Generation{}, errors.New(ie.Message)
			}
			return training.Generation{}, err
		}
		return training.Generation{Response: out.Response, InputTokens: out.InputTokens, OutputTokens: out.OutputTokens}, nil
	}
}

func trainingNames(evals []training.Eval) []string {
	names := make([]string, len(evals))
	for i, e := range evals {
		names[i] = e.Name
	}
	return names
}

// printEvals prints the evals with their cases, as a table.
func printEvals(w *os.File, evals []training.Eval) {
	colors := ansi.New(w)
	for _, e := range evals {
		var names []string
		for _, c := range e.Cases {
			names = append(names, c.Name)
		}
		fmt.Fprintf(w, "  %s  %d cases  %s\n", colors.Yellow(e.Name), len(names), strings.Join(names, ", "))
	}
}
