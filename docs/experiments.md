# Eval experiments

An **experiment** is a named series of eval runs. Every time the eval runner (`go run ./evals`, `./run_evals.sh`) executes, it records one **run** of an experiment in the **eval** database (`bin/gligabao-eval.db`), which keeps the evaluation data apart from the main database and is never reset. Each run works in datasets of its own (`<case>_run<run id>`), so runs do not interfere with each other. A run records which evals of which suites it executed, how each ended, and the IDs of the [ATIF trajectories](evals.md) of their calls. Go code lives in `internal/experiments`; the tables are in `internal/db/relational/{sqlite,postgres}/db_schema.go`, the structs in `internal/db/relational/model`.

## Data model

| table | struct | meaning |
|---|---|---|
| `gb_experiments` | `Experiment` | `name` (unique), `description`, `config` (free-form JSON: the settings under test) |
| `gb_experiment_evals` | `Experiment.Evals` (`EvalRef`) | the evals (`suite` + case `eval_name`) that any run of the experiment executed; added the first time an eval is recorded |
| `gb_experiment_runs` | `ExperimentRun` | one execution: `status` (`running`, then `passed` or `failed`), `params` (how it was invoked), totals (`total_evals`, `passed_evals`, `failed_evals`, `cost_usd`), `started_at`, `finished_at` |
| `gb_experiment_run_evals` | `ExperimentRunEval` | one eval's result in a run: `status` (`passed`, `failed`: a check did not hold, `error`: it could not run to the end), `answer`, `problems` (failed checks), `passed_checks`, `dataset_id` (the dataset the eval ran in, which holds the knowledge graph it built; null if it was never created), `error`, `steps`, `cost_usd`, times |
| `gb_experiment_trajectories` | `ExperimentTrajectory` | the ATIF trajectories of an eval, in call order (`seq`): `kind` (`remember` / `recall`), `trajectory_id`, `agent_name`, `steps`, `cost_usd`, `path` of the JSON file, `artifact_ids` (the IDs, in the eval's dataset, of the call's artifacts: the artifact a remember stored, or the artifacts a recall's evidence edges were sourced from via the edge provenance links; `experiments.GetRun` and the admin API resolve them to `artifacts` by reading the dataset) |

The graph and the artifacts of an eval are not copied into the experiment tables: they are read from the dataset the eval ran in, via `dataset_id` and `artifact_ids`. Datasets are only ever soft-deleted, so they stay readable; if a dataset (or an artifact) is purged from the eval database, its eval shows no graph (or no artifact).

A suite is a file in `evals/suites/` and its name is the file name without `.json`; an eval is a case in it, so `suite` + `eval_name` identifies it. Rows cascade: deleting an experiment removes its runs, evals and trajectory references.

Only the trajectory *reference* is stored in the database; the trajectory is the file `<trajectory_id>.json` in the trajectory directory. The runner stores the files in an `atif/` directory next to the eval database (`bin/atif/` by default) so the IDs stay resolvable; `-atif <dir>` changes it. `gb_experiment_trajectories.path` records where each file went.

A run is `passed` when every recorded eval passed. An eval fails when a stored-edge check does not hold or the answer lacks an expected string (`problems` lists them).

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
_, err = t.Record(ctx, experiments.EvalResult{
    Suite: "basic", Name: "job_change", Answer: answer,
    Problems: problems,              // failed checks; PassedChecks: the ones that held; Err: why it could not run
    DatasetID: dsID,                 // the dataset the eval ran in (holds its graph and artifacts)
    ArtifactIDs: artIDs,             // per trajectory: the IDs of its artifacts in that dataset
    Trajectories: trajs, TrajectoryDir: dir,
})
run, err := t.Finish(ctx)            // totals the evals, sets passed / failed
```

`d` is the eval database's `Relational`. Reading: `experiments.Get(ctx, d, name)` (with `Evals`), `List`, `Runs(ctx, d, experimentID)`, `GetRun(ctx, d, runID)` (evals and trajectory references included, with each trajectory's `Artifacts` read from the eval's dataset), `RunEvalDataset(ctx, d, runEvalID)` (the dataset an eval ran in; `memory.ExportDatasetGraph` reads its graph) and `FindTrajectory(ctx, d, trajectoryID)`, which leads from a trajectory ID back to its eval (`RunEvalID`), run and experiment. `Record` fails with `ErrRunNotRunning` after `Finish`.

## Admin UI

With the admin UI enabled (`serve --admin-ui`, see the README), the **Evals** block shows the experiments of the eval database (`--eval-db`; without it the block has nothing to show): totals (experiments, runs, distinct evals, passed/total eval results, cost), the experiment list and, by clicking an experiment, its runs. Clicking a run opens its evals; expanding an eval shows its answer, all its checks (passed and failed) and error, plus its ATIF traces. Each eval also has a **View graph** button that opens the knowledge graph it built, read live from the eval's dataset, in the same graph view as the datasets' **View graph** (`showGraphAt` in `graph.ts`). Each trace also has a **View artifacts** button: the stored artifact for a `remember` call, the source artifacts of the answer for a `recall`; they are read from the eval's dataset. **View trace** draws a trajectory as a labeled process flow (d3): start → steps (system / user / agent; edges are labeled with the time between steps) → tool calls (`calls`) → tool results (`returns`) → end. Clicking a node shows its full content (message, reasoning, arguments, result, tokens, cost).

The JSON behind it is under `/v1/gb/admin/api/evals`: `GET /` (overview), `GET /experiments/{id}`, `GET /runs/{id}`, `GET /run-evals/{id}/graph` (the graph of the eval's dataset) and `GET /trajectories/{trajectory_id}`. A trajectory is served only if an experiment recorded its ID. Its file is read from the `atif/` directory next to the eval database, then from the `path` stored with the reference, so the server must be able to read the directory the evals wrote to.
