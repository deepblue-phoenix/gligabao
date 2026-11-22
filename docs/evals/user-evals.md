# User evals

Every user of a gligabao server can have **evals**: a set of cases (texts to remember, a question, and what the answer and the stored facts must be), run against the real LLM. The evals are always on (there is no setting for them). A user's evals are JSON files, and their runs are executed in the server, one **eval run** per execution, recorded in the server's main database under the user.

The unit of the user API is the **eval** (an eval def, named by the user) and its **runs** (eval runs). Experiments (groups of evals, see [experiments.md](experiments.md)) stay in the schema, the developer runner and the admin UI, but have no user endpoint. The cases, the checks and the trajectories are as in [knowledge.md](knowledge.md); the evals of trained models are in [training.md](training.md) (admin only, owner `system`).

## The files

An eval is a JSON file, kept in the **meta bucket** (S3) under `evals/<user>/<name>.json`; the corpus files a case names with `text_file` are at `evals/<user>/<path>`. Without a bucket the same layout is in a local directory (`<dir>/<user>/<name>.json`). The user is encoded as exactly one path segment, and every key is built in code from the user and a validated relative path.

| setting | env | default | |
|---|---|---|---|
| `evals_s3_bucket` | `EVALS_S3_BUCKET` | empty | the meta bucket; empty = local directory |
| `evals_s3_region` / `evals_s3_endpoint` | `EVALS_S3_REGION` / `EVALS_S3_ENDPOINT` | empty | region (SDK default when empty), custom endpoint (MinIO, LocalStack) |
| `evals_s3_prefix` | `EVALS_S3_PREFIX` | `evals` | subdirectory of the bucket |
| `evals_local_dir` | | `evals_user` next to the database | local fallback |

The format is the one of the files of `build/evals_knowledge/` (`name`, `description`, `cases`, each with `remember` items (`text` or `text_file`, `reference_time`, `web_search`, `timeseries`), `query`, the `expect_*` checks, `chunk_size`, `models`, `reasoning`); `name` must equal the name in the path. The file is validated as a whole and every problem is returned:

- eval and case names match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, without `..`; case names are unique in the eval; `runs`, `case_runs` and `trajectories` are reserved (they are route words), and the user `system` is refused (`400`);
- a `text_file` is a relative path (`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`, no `..`, no `//`), and must exist (checked on save and again when a run starts); a remember item has `text` or `text_file`, not both;
- limits: file <= `evals_max_file_bytes` (1 MiB), cases <= `evals_max_cases` (100), remember items per case <= 20, one text or corpus file <= `evals_max_text_bytes` (256 KiB), query <= 4 KiB, 50 corpus files per user; `models` and `reasoning` only from the price table and the reasoning levels; `chunk_size` valid for the chunker.

The S3 JSON is the source of truth. A save validates, writes the JSON, then upserts the def into the database (`runner.UpsertEvalDef`); if the upsert fails, a new JSON is removed again (an old one is put back) and the answer is `500`. A run **re-reads the JSON and upserts again** when it starts, and snapshots it (each case run records the `input` it ran with), so changing the eval during a run does not change that run. S3 being down gives `503` `{"error":"eval storage unavailable"}`.

The code is `internal/evals/userfiles` (the store: `S3` and `FS`, which refuses paths that escape the user's directory, symlinks included), `internal/evals/evalfile` (`ValidateUser`) and `internal/evals/usereval` (save, get, list, delete, files).

## API

The routes are under `/v1/gb/evals` and use the same `gb-user-id` rule as the other user routes. An id or name of another user is `404`, like a missing one. Errors are `{"error": "..."}`; validation errors are `{"error":"invalid eval","problems":["..."]}`. (gloflow_emergence only lets `/v1/gb/evals/**` through to users who are `ai_enabled`.)

| Route | Body / query | Success | Errors |
|---|---|---|---|
| `GET /v1/gb/evals` | | `200 {"evals":[{name, description, cases, runs, runs_by_status, cost_usd, last_run, updated_at}], "totals":{evals, runs, cases, counts, cost_usd}}` | |
| `PUT /v1/gb/evals/{name}` | the eval JSON | `201` (new) or `200` `{name, cases, updated_at}` | `400`, `413`, `500`, `503` |
| `GET /v1/gb/evals/{name}` | | `200` the stored JSON, plus `files` and `updated_at` | `404` |
| `DELETE /v1/gb/evals/{name}` | `?files=true` also deletes the corpus files | `204`: its runs, their eval datasets, the def and the JSON | `404`, `409` a run is active |
| `PUT /v1/gb/evals/{name}/files/{path...}` | file bytes (`text/plain` or `application/json`, <= 256 KiB) | `204` | `400`, `404`, `413` |
| `DELETE /v1/gb/evals/{name}/files/{path...}` | | `204` | `404` |
| `POST /v1/gb/evals/{name}/runs` | `{"cases":[names]?, "models":{}?, "reasoning":{}?, "geo":bool?}` | `202 {"run_id","status":"pending"}` | `400`, `404`, `429` |
| `GET /v1/gb/evals/{name}/runs?limit=` | default 50, max 200 | `200 {"runs":[RunView]}`, newest first | `404` |
| `GET /v1/gb/evals/runs/{id}` | | `200 RunView` plus `cases` (the case runs, with their trajectory references) | `404` |
| `POST /v1/gb/evals/runs/{id}/cancel` | | `202 {"run_id","status"}` | `404`, `409` finished |
| `DELETE /v1/gb/evals/runs/{id}` | | `204`: the run, its case runs and datasets | `404`, `409` active |
| `GET /v1/gb/evals/case_runs/{id}` | | `200 CaseRunView` | `404` |
| `GET /v1/gb/evals/case_runs/{id}/graph` | | the knowledge graph of the case's eval dataset | `404`, `410` |
| `GET /v1/gb/evals/case_runs/{id}/temporal` | | the temporal plot data | `404`, `410` |
| `GET /v1/gb/evals/case_runs/{id}/debug` | | checks, answer, expectations, settings, traces, commentaries, reruns | `404` |
| `POST /v1/gb/evals/case_runs/{id}/commentary` | `{"model"?}` | an LLM [commentary](knowledge.md#eval-commentary-debugging-a-failed-case) on a failed case | `400`, `404`, `429`, `502`, `503` |
| `POST /v1/gb/evals/bookmarks` | `{kind, target_id, edge_id?, fact?, note?}` | bookmarks something of the eval results to review later; `kind` is `experiment_run`, `eval_run`, `case_def`, `case_run` (`target_id` is its id) or `fact` (`target_id` is the case run, with `edge_id` and `fact: {source, relation, target, description?}`). The levels above are filled by the server. Again: updates the note. `201` with the bookmark | `400`, `404` |
| `GET /v1/gb/evals/bookmarks` | | the user's bookmarked facts, newest first, as `{bookmarks}`; each has `kind`, the ids of its levels, `case_name`, `eval_name`, `experiment_name`, `verdict` and the run `params` (models) | |
| `DELETE /v1/gb/evals/bookmarks/{id}` | | removes a bookmark (`204`) | `404` |
| `POST /v1/gb/evals/case_runs/{id}/rerun` | models and thinking levels, as in [Rerunning a case](experiments.md#rerunning-a-case-with-other-models) | `202` | `400`, `404`, `409`, `429`, `503` |
| `GET /v1/gb/evals/trajectories/{id}` | | the ATIF JSON | `404` |
| `GET /v1/gb/evals/{name}/history?case=NAME` | | `200 {"eval","name","points":[...]}` over the eval's runs | `404` |

`RunView` has `id, eval, status, error, params, counts, cost_usd, started_at, finished_at, created_at, cancel_requested`; `CaseRunView` is the case run of the admin API (`internal/api/evals/views.go`, shared by both APIs). The routes `/evals/{name}/runs` and `/evals/{name}/history` are one pattern (`/evals/{name}/{sub}`) that picks by the last segment, because Go's mux does not accept them next to `/evals/runs/{id}`. The handlers are in `internal/api/evals`.

## Runs

A `POST .../runs` records the run as `pending` and returns at once; the run manager (`internal/evals/runmanager`) starts it in the background: it claims the run (`running`, with its `runner_id`, `<hostname>:<pid>:<start time>`), runs the cases in process (`remember.Remember` and `query.Run`, see [Eval calls](knowledge.md#eval-calls)), with the server's database and vector store, as the user, and ends it `completed`, `errored` or `cancelled`. Status and verdict are as in [experiments.md](experiments.md#data-model).

- **Liveness.** A running run beats every `evals_heartbeat_interval` (15 s); the beat also reads `cancel_requested`, set by the cancel endpoint, and cancels the run's context. A server shutdown ends its runs `cancelled` (`server shutdown`). A run whose last beat is older than `evals_heartbeat_stale` (2 min) is stale: a sweep at startup and every minute ends it `errored` with `server restarted or crashed while the run was in progress`. This works with several servers on one database, since only runs with another `runner_id` that went silent are stale (a CLI run has no `runner_id` and is never stale).
- **Limits.** Runs in progress: `evals_max_runs_per_user` (2) and `evals_max_runs` (8), else `429` `{"error":"too many runs in progress"}`. A run stops, ending `errored` with `cost cap reached`, when its cases cost more than `evals_max_run_cost_usd` (5 USD); a user whose runs, reruns and commentaries cost more than `evals_max_user_cost_usd_per_day` (20 USD) in the day gets `429`. `models` and `reasoning` only from the supported ones; `geo` defaults to true (`web_search` is per remember item of the eval).
- **Remember manager.** The eval runs use their own [remember manager](../remember/remember-manager.md), which serializes the evals of all users (its concurrency is not configurable yet).
- **Trajectories.** Stored under the owner: `traces/atif/<owner>/` in S3, or `evals_trajectory_dir` (default `atif` next to the database) with the file store. Reading one checks the owner of the case run first, and refuses a location of another user's directory.

## Eval datasets

The knowledge a case builds goes to an **eval dataset**: `gb_datasets.kind = 'eval'`, named `r<eval run id>-<case>` (cut to fit 64 bytes), with `eval_run_id` set. Every regular dataset function sees only `kind = 'data'`, so the eval datasets are not in `GET /v1/gb/datasets`, `DELETE /v1/gb/datasets/{name}` answers `404` for one, and a `remember` into that name makes a new `data` dataset (the unique index is `(user_id, kind, name)`). The eval handlers reach them through `memory_dataset.WithEval` (`memory_core.WithEvalRun`). The admin API lists them with a `kind` field and never deletes one through the dataset route.

Deleting a run or an eval deletes its datasets and their vectors at once (`memory_dataset.DeleteEvalDatasets`). Otherwise an hourly sweep deletes those of runs finished more than `evals_dataset_retention_days` (14) ago; the run and the case runs stay, and their graph and temporal views answer `410` `{"error":"the graph of this run was deleted (retention)"}`.

## Owners in the database

Every universal evals table (`gb_eval_defs`, `gb_experiment_defs`, `gb_experiment_runs`, `gb_eval_runs`, `gb_case_runs`) has `user_id` (not empty); names are unique per user, and composite foreign keys keep a run and its def, and a case run and its run, under the same owner (the database refuses a mismatch). The other tables are reached through an owner-checked parent. Every read of `db.Store` takes the owner and filters on it (an empty owner is `ErrInvalidOwner`, never "all users"; someone else's row is `ErrNotFound`); the admin reads (`AdminListEvalDefs`, `AdminListEvalRuns`, `AdminOwnerOf`, ...) are separate and may span users. `gb_eval_runs` also has `runner_id`, `heartbeat_at` and `cancel_requested`. The tables are in [../listings/db-tables.md](../listings/db-tables.md). There is no migration: the old (empty) evals tables of a deployed database are dropped and recreated by a guard in `evals.Open`, and it refuses, telling the operator, when they have rows.

## Admin

The admin API (`/v1/gb/admin/api/evals/**`, never proxied) reads the same tables for all users: the list takes `?user_id=`, and the views carry `user_id`. It wraps the same handlers as the user API (`admin.AdminOwner` looks the owner of the def, run, case run or trajectory up, then reads as that owner). See [experiments.md](experiments.md#admin-ui).

## The developer runner

`./run_evals.sh -user <user>` (see [`internal/evals/README.md`](../../internal/evals/README.md)) runs the files of `evals_knowledge_dirs` on a database of its own, as an owner given with `-user`. It is a developer tool; the server does not read those directories.
