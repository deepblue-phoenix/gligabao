// Command knowledge runs the knowledge evals end to end against the real LLM.
//
// Each eval in the *.json files of the eval directories (evals_knowledge_dirs of the config file -config, build/gligabao.toml by default; or
// -evals, repeatable, which wins; build/evals_knowledge if neither is set; an eval of an earlier directory hides one of the same name in a
// later one) is a JSON object with a "name" and a list of "cases". The evals are put in the
// eval database (-db) as eval defs, with a case def for each case (rewritten if the file changed), and run from there. The database is
// never reset: every run of a case has a dataset of its own, named after the case and the eval run (see knowledge.DatasetName), so
// runs do not interfere with each other and the graphs of all runs are kept. The cases are run behind an in-process HTTP server in
// eval mode through the eval endpoints (/v1/gb/eval/remember and /v1/gb/eval/query): remember (once per text, in order), then
// query. These return the ATIF trajectory of every call along with its result.
//
// Usage: go run ./internal/evals/knowledge/cmd [-v] [-config file] [-evals dir ...] [-eval name] [case-name ...]   (requires ANTHROPIC_API_KEY)
// With -eval, only the cases of that eval run; -list-evals prints the available evals and exits.
// With -v, every LLM call's input and output is printed.
// With -atif dir, the trajectories are stored there as JSON (default: an "atif" directory next to the -db file, since the case runs
// reference them).
//
// Every eval is an eval run, and every invocation is a run of an experiment (-experiment, default "default"; -experiment "" runs
// the evals on their own, without one), all stored in the eval database. The exit status is 1 if any case did not pass.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/evals/evalfile"
	"github.com/deepblue-phoenix/gligabao/internal/evals/knowledge"
	"github.com/deepblue-phoenix/gligabao/internal/evals/runner"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
)

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

func mainErr() error {
	verbose := flag.Bool("v", false, "print the input and output of every LLM call")
	var evalsFlag evalfile.DirsFlag
	flag.Var(&evalsFlag, "evals", "directory the evals (*.json) and their corpus files are read from; repeatable, searched in order (default: evals_knowledge_dirs of the config, else "+knowledge.DefaultEvalDir+")")
	configPath := flag.String("config", evalfile.DefaultConfigPath, "config file whose evals_knowledge_dirs lists the eval directories (relative paths in it are relative to the file)")
	eval := flag.String("eval", "", "run only the cases of this eval (its name as set in the JSON file; see -list-evals)")
	listEvals := flag.Bool("list-evals", false, "print the available evals and their cases, then exit")
	atifDir := flag.String("atif", "", `store every ATIF trajectory as JSON in this directory (default: "atif" next to the -db file)`)
	experiment := flag.String("experiment", "default", `name of the experiment this run belongs to (created if new); "" runs the evals on their own`)
	experimentDesc := flag.String("experiment-desc", "", "description of the experiment, used when it is created")
	evalDBPath := flag.String("db", "build/gligabao-eval.db", "SQLite file of the eval database: the datasets of the cases, the evals and their runs (never reset; show it with ./run_server_local.sh)")
	modelsFlag := flag.String("models", "", `LLM model per stage, e.g. "extract_graph=claude-sonnet-4-5,query=claude-haiku-5-5" (stages: `+strings.Join(knowledge.ModelStages(), ", ")+`; query is the call that answers the question, and detect_geo applies to remember and query); a stage left out uses the default model; a case's "models" win`)
	reasoningFlag := flag.String("reasoning", "", `reasoning level per stage, e.g. "extract_graph=low,query=medium" (stages: `+strings.Join(knowledge.ReasoningStages(), ", ")+`; levels: `+strings.Join(ai.ReasoningLevels, ", ")+`); a stage left out uses the server default (none); a case's "reasoning" wins`)
	flag.Parse()

	configSet := false
	flag.Visit(func(f *flag.Flag) { configSet = configSet || f.Name == "config" })
	evalsDirs, err := evalfile.ResolveDirs(evalsFlag, *configPath, configSet, func(c *config.Config) []string { return c.EvalsKnowledgeDirs }, knowledge.DefaultEvalDir)
	if err != nil {
		return err
	}
	loaded, err := knowledge.LoadEvals(evalsDirs...)
	if err != nil {
		return err
	}
	if *listEvals {
		printEvals(os.Stdout, loaded)
		return nil
	}
	selected, err := knowledge.SelectEvals(loaded, *eval, flag.Args())
	if err != nil {
		return err
	}
	models, err := knowledge.ParseModels(*modelsFlag)
	if err != nil {
		return fmt.Errorf("-models: %w", err)
	}
	reasoning, err := knowledge.ParseReasoning(*reasoningFlag)
	if err != nil {
		return fmt.Errorf("-reasoning: %w", err)
	}
	if *evalDBPath == "" {
		return errors.New("-db must not be empty")
	}
	if *atifDir == "" {
		// The case runs reference the trajectories by ID, so keep their files next to the eval database.
		*atifDir = filepath.Join(filepath.Dir(*evalDBPath), "atif")
	}
	memory_core.TrajectoryDir = *atifDir

	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY must be set to run evals")
	}
	llm, err := ai.New(key)
	if err != nil {
		return err
	}
	// remember embeds the entities and edges it stores into the eval database's vector store (see docs/vectors.md); without an
	// embedder it stores none, so say so. emb stays a nil interface then.
	var emb memory_core.Embedder
	if e, err := ai.NewEmbedder(os.Getenv(config.EnvOpenAIKey), ai.WithEmbedDimensions(config.DefaultEmbeddingDimensions)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: no embedder (%v): entities and edges will NOT be vector indexed\n", err)
	} else {
		emb = e
	}

	database, err := db.Open(db.Config{SQLitePath: *evalDBPath})
	if err != nil {
		return err
	}
	defer database.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	store, err := evals.StoreFor(database.Backend)
	if err != nil {
		return err
	}
	r := runner.New(database.Relational, store)

	// The evals as defs in the database.
	var defs []*core.EvalDef
	var defIDs []core.EvalDefID
	for _, e := range selected {
		def, err := r.SyncEval(ctx, knowledge.Spec(e))
		if err != nil {
			return err
		}
		defs, defIDs = append(defs, def), append(defIDs, def.ID)
	}

	params := core.Params{"evals_dirs": evalsDirs, "eval": *eval, "cases": flag.Args(), "atif_dir": *atifDir}
	if len(models) > 0 {
		params["models"] = models
	}
	if len(reasoning) > 0 {
		params["reasoning"] = reasoning
	}
	var xr *runner.ExperimentRecorder
	var xdef *core.ExperimentDef
	if *experiment != "" {
		if xdef, err = r.EnsureExperiment(ctx, *experiment, *experimentDesc, defIDs); err != nil {
			return err
		}
		if xr, err = r.StartExperimentRun(ctx, xdef, params); err != nil {
			return fmt.Errorf("start experiment: %w", err)
		}
	}

	x := &knowledge.Executor{
		DB: database, LLM: llm, Embedder: emb, WebSearch: llm, EvalsDirs: evalsDirs, Models: models, Reasoning: reasoning,
		TrajectoryDir: *atifDir, Verbose: *verbose, Out: os.Stdout,
	}
	var counts core.Counts
	var runErr error
	for _, def := range defs {
		opts := runner.EvalOptions{Params: params, Meta: &core.KnowledgeEvalRunMeta{}}
		if xr != nil {
			opts.ExperimentRunID = &xr.Run().ID
		}
		fmt.Printf("--- eval %s (%d cases)\n", def.Name, len(def.Cases))
		run, err := r.RunEval(ctx, def, x, opts)
		if run != nil {
			counts.Passed += run.Counts.Passed
			counts.Failed += run.Counts.Failed
			counts.Errored += run.Counts.Errored
			counts.Cancelled += run.Counts.Cancelled
			counts.Pending += run.Counts.Pending
			counts.Total += run.Counts.Total
			if xr != nil {
				if aerr := xr.Add(context.WithoutCancel(ctx), run); aerr != nil {
					fmt.Fprintf(os.Stderr, "warning: record eval %s in experiment %q: %v\n", def.Name, *experiment, aerr)
				}
			}
		}
		if err != nil {
			runErr = err
			break
		}
	}

	failed := counts.Failed + counts.Errored + counts.Cancelled + counts.Pending
	fmt.Printf("---\npassed: %d, failed: %d\n", counts.Passed, failed)
	if xr != nil {
		if err := xr.Finish(ctx, runErr); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		} else {
			run := xr.Run()
			fmt.Printf("experiment %q run %d: %s, cost $%.6f\n", xdef.Name, run.ID, run.Status, run.CostUSD)
		}
	}
	if runErr != nil {
		return runErr
	}
	if failed > 0 {
		os.Exit(1)
	}
	return nil
}

// printEvals prints the evals with their cases, as a table.
func printEvals(w *os.File, evals []knowledge.Eval) {
	bold, dim, reset := "", "", ""
	if st, err := w.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == "" {
		bold, dim, reset = "\033[1m", "\033[2m", "\033[0m"
	}
	width := 0
	for _, e := range evals {
		width = max(width, len(e.Name))
	}
	for _, e := range evals {
		var names []string
		for _, c := range e.Cases {
			names = append(names, c.Name)
		}
		noun := "cases"
		if len(names) == 1 {
			noun = "case "
		}
		fmt.Fprintf(w, "  %s%-*s%s  %s%2d %s%s  %s\n", bold, width, e.Name, reset, dim, len(names), noun, reset, strings.Join(names, ", "))
	}
}
