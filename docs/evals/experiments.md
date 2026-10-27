# Eval experiments

An **experiment** is a named group of evals that are run together, plus the settings under test. Every time the eval runner (`go run ./internal/evals/knowledge/cmd`, `./run_evals.sh`) executes, it records one **run** of an experiment in the **eval** database (`build/gligabao-eval.db`), which keeps the evaluation data apart from the main database and is never reset. Each case run works in a dataset of its own (`<case>_run<eval run id>`), so runs do not interfere with each other. A run records which cases of which evals it executed, how each ended, and the IDs of the [ATIF trajectories](knowledge.md) of their calls. The code is [`internal/evals`](../../internal/evals/README.md): the structs are in `core`, the tables in `db/{sqlite,postgres}/schema.go`, and the recording of the runs in `runner`.

## Data model

The knowledge-extraction evals and the [evals of trained models](training.md) share one set of tables, the `gb_*` tables. The **universal** tables hold what every eval has and carry a `type` column (`knowledge` or `training`); what only one type has is in that type's **metadata** tables, one row per row of the universal table. A composite foreign key `(parent id, type)` keeps the type the same down the chain: the database refuses a case def under an eval of the other type, a case run under an eval run of the other type, or metadata on a row of the other type. The vocabulary and the rules of the model are in [`core/doc.go`](../../internal/evals/core/doc.go).

**Terms.** There are two sides, and every concept exists on both:

| definitions (what to run; stored, reused) | runs (what happened; append-only history) |
|---|---|
| `ExperimentDef`: which evals, plus settings | `ExperimentRun`: one execution of an experiment def |
| `EvalDef`: a named collection of cases of one type | `EvalRun`: one execution of an eval def |
| `CaseDef`: one input and what is expected or checked | `CaseRun`: one execution of a case def |

An *eval case* (a *case*) is one input/output combination, plus metadata, to be evaluated. Cases are grouped into an *eval* (a set of cases). An *experiment* groups evals (an eval can be in many experiments). The definitions are stored in the database when a run starts (`runner.SyncEval` puts the evals of the JSON files in as defs, and rewrites a case def whose file changed); a run has a link to its def, and records the `params` it was invoked with, so it can be understood without looking at the def as it is now.

| table | struct (package `core`) | meaning |
|---|---|---|
| `gb_eval_defs` | `EvalDef` | `name` (unique), `type`, `description`, `tags`, `params` |
| `gb_case_defs` | `CaseDef` | a case of an eval: `name` and `position` (both unique within the eval), `type`; its type-specific part is in `gb_case_def_meta_<type>` |
| `gb_case_def_meta_knowledge` | `KnowledgeCaseDefMeta` | the texts to `remember`, the `query`, the checks (`expect_contains`, `expect_edges`, `expect_min_chunks`, `expect_web_searches`, `expect_series`, `expect_geo`) and the settings (`chunk_size`, `models`, `reasoning`) |
| `gb_case_def_meta_training` | `TrainingCaseDefMeta` | `instruction`, `input` and the expected `output` |
| `gb_experiment_defs` | `ExperimentDef` | `name` (unique), `description`, `params` (free-form JSON: the settings under test) |
| `gb_experiment_def_evals` | `ExperimentDef.EvalDefIDs` | the evals of an experiment, in the order they run |
| `gb_experiment_runs` | `ExperimentRun` | one execution of an experiment: `status`, `error`, `params` (how it was invoked), `counts_*` and `cost_usd` over the case runs of all its eval runs, `created_at`, `started_at`, `finished_at` |
| `gb_eval_runs` | `EvalRun` | one execution of an eval, in an experiment run or on its own (`experiment_run_id` is NULL): `type`, `status`, `error`, `params`, `counts_*`, `cost_usd`, times |
| `gb_eval_runs_meta_knowledge` | `KnowledgeEvalRunMeta` | the statistics of the run as a whole: `chunks`, `steps`, `remember_calls`, `query_calls` and their cost, `checks_passed`, `checks_failed` |
| `gb_eval_runs_meta_training` | `TrainingEvalRunMeta` | the trained model, the judge and the split of the scores, see [training.md](training.md#tables) |
| `gb_case_runs` | `CaseRun` | one case's result in an eval run: `type`, `status`, `error`, `verdict`, `score`, `cost_usd`, times |
| `gb_case_runs_meta_knowledge` | `KnowledgeCaseRunMeta` | `input` (as given), `answer`, `problems` (the checks that failed), `passed_checks`, `dataset_id` (the dataset the case ran in, which holds the knowledge graph it built; null if it was never created), `steps` (ATIF steps over its trajectories) |
| `gb_case_runs_meta_knowledge_trajectories` | `KnowledgeTrajectory` | the ATIF trajectories of a case, in call order (`seq`): `kind` (`remember` / `query`), `trajectory_id`, `agent_name`, `steps`, `cost_usd`, `path` of the JSON file, `artifact_ids` (the IDs, in the case's dataset, of the call's artifacts: the artifact a remember stored, or the artifacts a query's evidence edges were sourced from via the edge provenance links; the admin API resolves them to `artifacts` by reading the dataset) |
| `gb_case_runs_meta_training` | `TrainingCaseRunMeta` | the question, the expected answer, the model's answer and the judge's score and reason, see [training.md](training.md#tables) |

Links between runs go from child to parent (`CaseRun.EvalRunID`, `EvalRun.ExperimentRunID`); a parent stores no list of its children. A def that has runs cannot be deleted (`ON DELETE RESTRICT`), so the history stays understandable; deleting a run deletes the runs under it and their metadata.

Both types have **evals**: a case belongs to an eval, and a case name is unique within its eval (the runner also requires them to be unique over all the evals, so a case can be named on the command line). Knowledge evals are the files of `build/evals_knowledge/`. Training evals are the files of `build/evals_training/` (named after the training dataset their cases ask about); an *eval run* (one run of the eval against one trained model) has one case run per case.

The graph and the artifacts of a case are not copied into the run tables: they are read from the dataset the case ran in, via `dataset_id` and `artifact_ids`. Datasets are only ever soft-deleted, so they stay readable; if a dataset (or an artifact) is purged from the eval database, its case shows no graph (or no artifact).

Only the trajectory *reference* is stored in the database; the trajectory is the file `<trajectory_id>.json` in the trajectory directory. The runner stores the files in an `atif/` directory next to the eval database (`build/atif/` by default) so the IDs stay resolvable; `-atif <dir>` changes it. `gb_case_runs_meta_knowledge_trajectories.path` records where each file went.

**Status and verdict.** The `status` of a run (experiment run, eval run or case run) is its *lifecycle*, never a verdict: `pending`, `running`, then `completed` (it ran to the end), `errored` (it could not run to the end; `error` says why) or `cancelled`. How it went is a separate field of a completed case run, its `verdict` (`pass` or `fail`) and its `score` (0 to 1). A run's `counts_*` are one count per status (`total`, `pending`, `running`, `completed`, `errored`, `cancelled`) and, of the completed case runs, `passed` and `failed`; every case is in exactly one of the five status counts, `pending` being the cases not reached (the ones left when a run is cancelled), and `total` is the number of cases the run is to execute. The schema's `CHECK`s enforce the sets.

| | eval run | case run |
|---|---|---|
| knowledge | `running`, then `completed`, `errored` (the executor could not go on) or `cancelled` (Ctrl-C) | `completed` with the verdict `pass` (every check held) or `fail` (a check did not hold, or the answer lacks an expected string; `problems` lists them), or `errored` (it could not be run to the end) |
| training | the same | `completed` with the verdict `pass` (the judge's score is at least 4 of 5) or `fail`, or `errored` (the model or the judge errored) |

A case that errors does not stop the run: it is an `errored` case run and the run goes on. The exit status of the knowledge runner is 1 if any case did not pass.

## Runner

```
./run_evals.sh [-experiment name] [-experiment-desc text] [-v] [case-name ...]
```

`-experiment` (default `default`) names the experiment; it is created on first use and later runs with the same name are further runs of it (`-experiment ""` runs the evals on their own, with no experiment run). The runner records into the eval database (`-db`, default `build/gligabao-eval.db`), which it never resets; the datasets of a case run are named `<case>_run<eval run id>`. It prints the status and cost of the run at the end.

## From Go

```go
r := runner.New(d, store)             // d: the eval database's *sql.DB; store: from evals.StoreFor(backend)
def, err := r.SyncEval(ctx, spec)     // put an eval of the files in the database as defs
xd, err := r.EnsureExperiment(ctx, "baseline", "...", []core.EvalDefID{def.ID})   // get-or-create the experiment def
xr, err := r.StartExperimentRun(ctx, xd, core.Params{"models": models})           // start a run of it
run, err := r.RunEval(ctx, def, executor, runner.EvalOptions{ /* ExperimentRunID, Params, ... */ })
err = xr.Add(ctx, run)                // the experiment run sums the counts and the cost of its eval runs
err = xr.Finish(ctx, nil)
```

`RunEval` records the eval run (`running`) and gives an `EvalRecorder` to the executor of the type (`knowledge.Executor`, `training.Executor`), which calls `StartCase` and `FinishCase` (or `RecordCases`) for each case and so rewrites the eval run as the cases finish. Reading is `db.Store` (`GetEvalRun`, `ListCaseRuns`, `ListExperimentRuns`, `ListCaseHistory`, `FindKnowledgeTrajectory`, ...): a case run comes with its metadata and, for a knowledge one, its trajectory references; `FindKnowledgeTrajectory` leads from a trajectory ID back to its case run, eval run and experiment.

## Admin UI

With the admin UI enabled (`serve --admin-ui`, see the README), the **Evals** block shows the experiments of the eval database (`--eval-db`; without it the block has nothing to show): totals (experiments, runs, distinct cases, the case runs by their counts, cost), the experiment list (with the runs of each by status) and, by clicking an experiment, its runs. The UI shows the model's own words, not statuses of its own: a run and a case run have the status of `internal/evals/core` (`pending`, `running`, `completed`, `errored`, `cancelled`), a case run also its verdict (`pass` or `fail`, none if it has none) and score, and a run its counts (`total`, `pending`, `running`, `completed`, `errored`, `cancelled`, `passed`, `failed`), which the JSON carries as `status`, `verdict`, `score` and `counts`. The history of a case plots its verdict (or its status when it has no verdict). Clicking a run opens its cases; expanding a case shows its answer, all its checks (passed and failed) and error, plus its ATIF traces. Each case also has a **View graph** button that opens the knowledge graph it built, read live from the case's dataset, in the same graph view as the datasets' **View graph** (`showGraphAt` in `graph.ts`). Each trace also has a **View artifacts** button: the stored artifact for a `remember` call, the source artifacts of the answer for a `query`; they are read from the case's dataset. **View trace** draws a trajectory as a labeled process flow (d3): start → steps (system / user / agent; edges are labeled with the time between steps) → tool calls (`calls`) → tool results (`returns`) → end. Clicking a node shows its full content (message, reasoning, arguments, result, tokens, cost).

The JSON behind it is under `/v1/gb/admin/api/evals`: `GET /` (overview), `GET /experiments/{id}`, `GET /runs/{id}`, `GET /runs/{id}/web-searches` (the [web searches](../web-search.md#admin-ui) each case made, read from its trajectories), `GET /case_runs/{id}/graph` (the graph of the case's dataset), `GET /experiments/{id}/history?eval=..&name=..` (the results of one case over the experiment's runs) and `GET /trajectories/{trajectory_id}`. A trajectory is served only if an experiment recorded its ID. Its file is read from the `atif/` directory next to the eval database, then from the `path` stored with the reference, so the server must be able to read the directory the evals wrote to.
