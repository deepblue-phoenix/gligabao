# The pipeline of `remember` and `query`

`remember` ([remember.md](remember.md)) and `query` ([query.md](query.md)) are two different operations, but they are built the same way: each is a **pipeline**, a fixed list of named **stages** that run one after the other on the state of one call, and each call leaves a **run log**, a row that says how every stage went. This page describes that common pattern. What each stage of each operation does is in its own page; the code of the pattern is the package `internal/memory/memory_pipeline`.

The image pipeline ([images.md](images.md)) does not use it, and has no run log.

## The pattern in one picture

```
 request ──► setup ──► pipeline (a list of stages) ──► result
             │          │
             │          ├─ runner: for every stage, tells the run log: start, done(err), skip
             │          └─ runner: gives every stage a context that files its inferences under it
             └─ validation, options, trace, run log row (status started)
```

1. **Setup** (`Remember`, `Query`): validates the request, resolves the dataset, the session, the models and reasoning of each stage (`memory_core.CallOptions`), starts the trace of the call and creates the **run log** row. A request rejected here (a `400`, an unknown dataset) has no run; one that was not rejected has.
2. **The pipeline**: a value, a list of steps, run by `memory_pipeline.Run`. It is where the work is.
3. **Finish**: the deferred `run.Finish(err)` closes the run, whatever happened: `succeeded` if the call returned a result, `failed` (with the error) if it returned an error, and no stage is left open.

The two operations compared:

| | `remember` | `query` |
| - | ---------- | ------- |
| Declared in | `memory_remember/remember_pipeline.go` (`rememberPipeline`, `reconcileSteps`) | `memory_query/query.go` (`queryPipeline`) |
| Stages | `classify_complexity`, `chunk_text`, `extract_graph`, `summarize_chunk`, `load_candidates`, `resolve_edges`, `store`, `store_series`, `resolve_geo`, `web_search`, `fetch_series`, `observe_series` | `retrieve_graph`, `detect_geo`, `nearby_geo`, `query` |
| State of the call | `job` in `memory_remember` | `job` in `memory_query` |
| Run log | `gb_remember_runs` (`memory_remember/runlog.go`), [run log](remember.md#run-log) | `gb_query_runs` (`memory_query/runlog.go`), [run log](query.md#run-log) |
| Response field | `remember_run_id` | `query_run_id` |
| Stage names and columns | `relational.RememberStages` | `relational.QueryStages` |
| Stage outputs (for debugging) | yes, `gb_remember_run_stage_outputs` ([stage outputs](remember.md#stage-outputs)) | no |
| Executed by | the [remember manager](remember-manager.md), one run at a time | the HTTP handler's goroutine, concurrently |
| Admin UI | the **remember** view | the **query** view |

## Declaring a pipeline

A pipeline is a `[]memory_pipeline.Step[S]`, where `S` is the **job**: the struct that holds the state of one call. The list reads top to bottom as the operation does; its comments say what each stage is for. Query's, in short:

```go
var queryPipeline = []memory_pipeline.Step[job]{
	memory_pipeline.Stage[job]{Name: relational.QueryStageRetrieveGraph, Run: (*job).retrieveGraph},
	memory_pipeline.Stage[job]{Name: relational.QueryStageDetectGeo, Policy: memory_pipeline.Advisory, Enabled: (*job).hasGeo, Run: (*job).detectGeo},
	memory_pipeline.Stage[job]{Name: relational.QueryStageNearbyGeo, Policy: memory_pipeline.Advisory, Enabled: (*job).hasNearbyGeo, Run: (*job).nearbyGeo},
	memory_pipeline.Stage[job]{Name: relational.QueryStageQuery, Run: (*job).answer},
}
```

A **stage** (`memory_pipeline.Stage`) has:

| Field | Meaning |
| ----- | ------- |
| `Name` | the stage, exactly as the run log names it (a constant of `relational`, or of `memory_core` if both operations use it, like `extract_graph` or `web_search`) |
| `Run` | the work: a method of the job, `func (j *job) stage(ctx context.Context) error`. It reads what earlier stages left in the job and leaves what later ones need |
| `Policy` | what happens if `Run` returns an error: `Fatal` (the default: the error ends the pipeline and is the error of the call; the stages after it stay `not_started`) or `Advisory` (the error is recorded on the stage, and the pipeline goes on: the stage is nice to have, like geo and web search) |
| `Enabled` | does this call use the stage? `nil` means always. A stage that is not enabled is not run, and is recorded as `skipped` |
| `Also` | other stages that this one does too, which start and end with it (`store` also stores the series of a text of one chunk, so `store_series` has the times of `store`) |

Two more things shape the flow:

- **`memory_pipeline.Stop`**: a stage returns it to end the pipeline early **without an error**. The stage ends as `succeeded`, the stages after it are `skipped`, and the call returns normally. `query` uses it when nothing was retrieved: there is nothing to answer from, so the fixed "no information" answer is the result and the LLM is not called.
- **`memory_pipeline.Each`**: runs a group of stages once for every item of the call (every chunk of a document), each item to the end before the next. An `Each` is not a stage of the run log; the stages inside it are, and they start again for each item (a stage keeps its first start time, so it spans all the items). `remember` uses it to extract and summarize every chunk, and then to reconcile and store them one after the other, so that a chunk is compared with the facts the earlier chunks stored.

A group of stages that is used in more than one place is a list of its own: `reconcileSteps` (`load_candidates`, `resolve_edges`, `store`) is run by the `Each` of the pipeline for the text or each chunk, and, with `memory_pipeline.Run`, by the enrichment stages (`web_search`, `observe_series`) for the texts they make.

## What the runner does for every stage

`memory_pipeline.Run(ctx, steps, job, observer)` does, for each stage in order:

1. If an earlier stage returned `Stop`, or the stage is not `Enabled`: `observer.Skip(stage)` and go on.
2. `observer.Start(stage)` (and of its `Also` stages), and give the stage a context with an **inference sink**.
3. Call `Run`.
4. `observer.Done(err, stage)`. Then, depending on the error and the policy: end the pipeline with the error (`Fatal`), or go on (`Advisory`, or no error).

`Run` returns the error of the first `Fatal` stage that failed, and `nil` otherwise.

This is the point of the pattern: **the stages do not know the run log**. A stage cannot forget to open or close itself in the log, cannot leave itself open on an early `return`, and cannot be named wrong, because the runner does all of it, around the stage, from the list. Adding a stage is adding one line to the list (and its column group to the run table); the run log, the admin API and the admin UI follow.

### Inferences

A stage that calls a model (an LLM, an embedding request, a web search) makes **inferences** (`gb_inferences`, see [inferences.md](inferences.md)). The context the runner hands to a stage carries a sink (`memory_core.WithInferenceSink`), and `memory_core.RecordRun` (which every inference is recorded through) passes it the ID of each one. The sink is `observer.AddInference(stage, id)`, so every inference ends up in the `<stage>_inference_ids` of the stage that made it, in any goroutine of the stage, with no call to the log from the stage. A stage must therefore **use the context it is given** for its model calls (and pass it on); a call made with another context is not filed.

## The run log

Both run logs implement the `memory_pipeline.Observer` interface (`Start`, `Done`, `Skip`, `AddInference`), and have the same shape:

- **one row per execution** of the pipeline in a table of its own (`gb_remember_runs`, `gb_query_runs`): the call (user, dataset, `params` = how it was made), the **status of the call as a whole** (`started`, then `succeeded` or `failed` with an `error`) and, for **every stage of the pipeline**, the columns `<stage>_status`, `<stage>_error`, `<stage>_started_at`, `<stage>_finished_at` and, for stages that run a model, `<stage>_inference_ids`;
- the row is **created when the call starts and updated at every step**, so a call in progress can be watched, and a row that stays `started` belongs to a process that died;
- the status of a stage is one of `not_started` (the pipeline never reached it: an earlier stage failed, or the call is still on its way), `started`, `succeeded`, `failed` or `skipped` (the call does not use it, or the pipeline was ended with `Stop`). A run that succeeded has no stage left `not_started`;
- a stage that is `Advisory` can be `failed` in a run that is `succeeded`: to know whether such a stage worked, look at the stage, not at the status of the call;
- a `nil` log does nothing, and a row that cannot be written is logged: **the log never fails the call**;
- the stages come from a table of definitions (`relational.RememberStages`, `relational.QueryStages`: the name, and whether the stage runs models). Tests check that the stages of each pipeline are exactly those of its table (`TestPipelineStagesAreTheRunLogs`), so the list and the schema cannot drift apart.

The admin API serves both the same way: `GET /v1/gb/admin/api/remember-runs` and `GET /v1/gb/admin/api/query-runs` (counts by status, and the list, filtered and paged), and the [admin UI](#in-the-admin-ui) draws both with the same code.

Existing database files are not migrated when the stages of a log change: delete the file and the server recreates it.

## In the admin UI

The **remember** and **query** views (`admin_ui/src/runs.ts`) are the same view over the two logs: per database, how many runs are in each status (click a tile to filter by it), and the list of the runs, newest first. A run opens to its parameters (and, for a query, the question and the session) and a table with every stage: status, start, duration, inference IDs, error, and a **?** button that says what the stage does. For `remember` there is also an **Outputs** button per stage, which loads the [stage outputs](remember.md#stage-outputs); `query` keeps none. A run that succeeded but has a failed stage is flagged.

## Adding an operation or a stage

To add a **stage** to an existing pipeline:

1. Add its name to the stage table (`relational.RememberStages` or `QueryStages`, with `Model` true if it runs models) and the columns to the schema of both backends (SQLite and Postgres).
2. Write its work as a method of the job, using the context it gets for model calls; return `memory_pipeline.Stop` only to end the call early on purpose.
3. Add a `memory_pipeline.Stage` to the list, at its place, with `Policy: Advisory` if the call can succeed without it, and `Enabled` if only some calls use it.
4. Update the `?` text of the stage in the admin UI (`runs.ts`), the docs of the operation, and the changelog.

To add a whole **operation** with the same pattern: a package with its `job`, its list of stages and its `runLog` (the `Observer`, around a `relational` run type with its table), `Run` the list from the operation's entry point after the setup, and `Finish` the log with the error of the call in a `defer`.

## Where it is in the code

| | |
| - | - |
| `internal/memory/memory_pipeline/pipeline.go` | `Stage`, `Each`, `Policy`, `Stop`, `Observer`, `Run`, `Names` |
| `internal/memory/memory_core/memory_core.go` | `WithInferenceSink`, `RecordRun`, `NoteInference` |
| `internal/memory/memory_remember/remember_pipeline.go`, `runlog.go` | the remember pipeline and its run log |
| `internal/memory/memory_query/query.go`, `runlog.go` | the query pipeline and its run log |
| `internal/db/relational/model/{remember_run,query_run}.go` | the run types and their stage definitions |
| `internal/api/admin/{remember_runs,query_runs}.go` | the admin API |
| `admin_ui/src/runs.ts` | the admin views |
