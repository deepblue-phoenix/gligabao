# Eval experiments

An **experiment** is a named series of eval runs. Every time the eval runner (`go run ./internal/evals/knowledge`, `./run_evals.sh`) executes, it records one **run** of an experiment in the **eval** database (`bin/gligabao-eval.db`), which keeps the evaluation data apart from the main database and is never reset. Each run works in datasets of its own (`<case>_run<run id>`), so runs do not interfere with each other. A run records which cases of which evals it executed, how each ended, and the IDs of the [ATIF trajectories](knowledge.md) of their calls. Go code lives in `internal/evals/experiments`; the database code is in `internal/evals/db` (the tables are in `internal/evals/db/{sqlite,postgres}/db_schema.go`, the structs in `internal/evals/db/model`).

## Data model

The knowledge-extraction evals and the [evals of trained models](training.md) share one set of tables. The **universal** tables hold what every eval (knowledge or training) has and carry a `type` column (`knowledge` or `training`); what only one kind has is in that kind's **metadata** table, one row per row of `gb_experiment_case_results`. A composite foreign key `(parent id, type)` keeps the type the same down the chain: the database refuses a training run under a knowledge experiment, a result under a run of the other type, or metadata on a result of the other type. Experiment names are unique within a type, so a knowledge and a training experiment can have the same name. Everything on this page is the `knowledge` type; the functions of this package only see those rows.

**Terms.** An *eval case* (a *case*) is one input/output combination, plus metadata, to be evaluated (an input and what is expected or checked). Cases are grouped into an *eval* (a set of cases). A *run* executes some cases once, and a *result* is the outcome of one case in one run: a case has many results, one per run it was in. An *experiment* is a named series of runs. The case definitions live in the eval files; the database holds the names (`gb_experiment_cases_defs`) and the results (`gb_experiment_case_results`).

Universal tables:

| table | struct | meaning |
|---|---|---|
| `gb_experiments_defs` | `Experiment` | `type`, `name`, `description`, `config` (free-form JSON: the settings under test) |
| `gb_experiment_cases_defs` | `Experiment.Cases` (`CaseRef`) | the cases (`eval` + `case_name`) that any run of the experiment executed; added the first time a case is recorded |
| `gb_experiment_runs` | `ExperimentRun` | one execution: `type`, `experiment_id` (always set for knowledge), `status` (`running`, then: knowledge `passed` or `failed`; training `completed`, `error` or `cancelled`), `params` (how it was invoked), totals (`total_cases`, `passed_cases`, `failed_cases`, `error_cases`, `judged_cases`, `cost_usd`), `started_at`, `finished_at` |
| `gb_experiment_case_results` | `ExperimentCaseResult` | one case's result in a run: `type`, `eval`, `case_name`, `status` (knowledge: `passed`, `failed`: a check did not hold, `error`: it could not run to the end; training: `judged`: whatever the score, `error`: the model or the judge errored), `error`, `cost_usd`, times |
| `gb_experiment_case_runs_trajectories` | `ExperimentTrajectory` | the ATIF trajectories of a case, in call order (`seq`): `kind` (`remember` / `recall`), `trajectory_id`, `agent_name`, `steps`, `cost_usd`, `path` of the JSON file, `artifact_ids` (the IDs, in the case's dataset, of the call's artifacts: the artifact a remember stored, or the artifacts a recall's evidence edges were sourced from via the edge provenance links; `experiments.GetRun` and the admin API resolve them to `artifacts` by reading the dataset) |

Metadata tables (the struct fields are those of `ExperimentCaseResult`; the result's `Type` is `knowledge`):

| table | meaning |
|---|---|
| `gb_knowledge_case_meta` | `answer`, `problems` (failed checks), `passed_checks`, `dataset_id` (the dataset the case ran in, which holds the knowledge graph it built; null if it was never created), `steps` (ATIF steps over its trajectories) |
| `gb_training_eval_run_meta` (one row per training run), `gb_training_case_meta` (one row per training case result) | the training eval runs' own columns (trained model, eval, judge, counters) and the per-case outcomes (question, the expected answer, the model's, the judge's score). See [training/EVALS.md](training.md#tables) |

Both kinds have **evals**: a case (`case_name`) belongs to an eval (`eval`). Knowledge evals are the files of `bin/evals_knowledge/`. Training evals are the files of `bin/evals_training/` (named after the training dataset their cases ask about); an *eval run* (one run of the eval against one trained model) is a run with one case result per case (`case_name` is the case's name, as for a knowledge case).

The graph and the artifacts of a case are not copied into the experiment tables: they are read from the dataset the case ran in, via `dataset_id` and `artifact_ids`. Datasets are only ever soft-deleted, so they stay readable; if a dataset (or an artifact) is purged from the eval database, its case shows no graph (or no artifact).

An eval is a file in `bin/evals_knowledge/` and its name is the file name without `.json`; a case is one of its cases, so `eval` + `case_name` identifies it. Rows cascade: deleting an experiment removes its runs, cases and trajectory references.

Only the trajectory *reference* is stored in the database; the trajectory is the file `<trajectory_id>.json` in the trajectory directory. The runner stores the files in an `atif/` directory next to the eval database (`bin/atif/` by default) so the IDs stay resolvable; `-atif <dir>` changes it. `gb_experiment_case_runs_trajectories.path` records where each file went.

A run is `passed` when every recorded case passed. A case fails when a stored-edge check does not hold or the answer lacks an expected string (`problems` lists them).

The status sets depend on the type (the schema's `CHECK`s enforce the sets). `failed` is a verdict about checks, so only knowledge has it; `error` means "could not be run to the end" in both types:

| | run | case |
|---|---|---|
| knowledge | `running`, then `passed` or `failed` | `passed`, `failed` (a check did not hold), `error` (could not be run to the end) |
| training | `running`, then `completed` (it ran to the end), `error` (the run itself errored) or `cancelled` | `judged`, `error` (the model or the judge errored) |

A training score is not a verdict: a judged case with a score of 0 is still `judged`. A knowledge run is `failed` when at least one case did not pass (a check did not hold, or it errored). The totals of a run are one count per case status and nothing else: `passed_cases`, `failed_cases`, `error_cases`, `judged_cases` (knowledge uses the first three, training the last two; the unused ones are 0). `total_cases` minus the four is the cases not reached (the examples left when a training run is cancelled).

## Runner

```
./run_evals.sh [-experiment name] [-experiment-desc text] [-v] [case-name ...]
```

`-experiment` (default `default`) names the experiment; it is created on first use and later runs with the same name are further runs of it. The runner records into the eval database (`-db`, default `bin/gligabao-eval.db`), which it never resets; the datasets of a run are named `<case>_run<run id>`. The runner prints `experiment "<name>" run <id>: <status>, cost $...` at the end.

## From Go

```go
t, err := experiments.Start(ctx, d, experiments.Options{
    Name: "baseline", Description: "...", Config: map[string]any{"model": "claude-haiku-4-5"},
    Params: map[string]any{"cases": []string{"job_change"}},
})                                   // get-or-create the experiment, start a run
_, err = t.Record(ctx, experiments.CaseOutcome{
    Eval: "basic", Name: "job_change", Answer: answer,
    Problems: problems,              // failed checks; PassedChecks: the ones that held; Err: why it could not run
    DatasetID: dsID,                 // the dataset the case ran in (holds its graph and artifacts)
    ArtifactIDs: artIDs,             // per trajectory: the IDs of its artifacts in that dataset
    Trajectories: trajs, TrajectoryDir: dir,
})
run, err := t.Finish(ctx)            // totals the cases, sets passed / failed
```

`d` is the eval database's `Relational`. Reading: `experiments.Get(ctx, d, name)` (with `Cases`), `List`, `Runs(ctx, d, experimentID)`, `GetRun(ctx, d, runID)` (its `CaseResults` and their trajectory references included, with each trajectory's `Artifacts` read from the case's dataset), `CaseResultDataset(ctx, d, caseResultID)` (the dataset a case ran in; `memory.ExportDatasetGraph` reads its graph) and `FindTrajectory(ctx, d, trajectoryID)`, which leads from a trajectory ID back to its case (`CaseResultID`), run and experiment. `Record` fails with `ErrRunNotRunning` after `Finish`.

## Admin UI

With the admin UI enabled (`serve --admin-ui`, see the README), the **Evals** block shows the experiments of the eval database (`--eval-db`; without it the block has nothing to show): totals (experiments, runs, distinct cases, passed/total case results, cost; the run tables also show the failed and errored counts), the experiment list and, by clicking an experiment, its runs. Clicking a run opens its cases; expanding a case shows its answer, all its checks (passed and failed) and error, plus its ATIF traces. Each case also has a **View graph** button that opens the knowledge graph it built, read live from the case's dataset, in the same graph view as the datasets' **View graph** (`showGraphAt` in `graph.ts`). Each trace also has a **View artifacts** button: the stored artifact for a `remember` call, the source artifacts of the answer for a `recall`; they are read from the case's dataset. **View trace** draws a trajectory as a labeled process flow (d3): start → steps (system / user / agent; edges are labeled with the time between steps) → tool calls (`calls`) → tool results (`returns`) → end. Clicking a node shows its full content (message, reasoning, arguments, result, tokens, cost).

The JSON behind it is under `/v1/gb/admin/api/evals`: `GET /` (overview), `GET /experiments/{id}`, `GET /runs/{id}`, `GET /runs/{id}/web-searches` (the [web searches](../web-search.md#admin-ui) each case made, read from its trajectories), `GET /case_results/{id}/graph` (the graph of the case's dataset), `GET /experiments/{id}/history?eval=..&name=..` (the results of one case over the experiment's runs) and `GET /trajectories/{trajectory_id}`. A trajectory is served only if an experiment recorded its ID. Its file is read from the `atif/` directory next to the eval database, then from the `path` stored with the reference, so the server must be able to read the directory the evals wrote to.
