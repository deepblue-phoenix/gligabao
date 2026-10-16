# Changelog

All notable changes to gligabao are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/) (while below 1.0.0: a minor bump may break things).

Newest entry first; its version must equal the contents of `VERSION` (checked by `go test`). See [AGENTS.md](AGENTS.md).

## [0.30.0] - 2026-10-26

### Added
- **Training API under `/v1/gb/training/`**, served whether or not the admin UI is on: `POST` and `GET /v1/gb/training/jobs` (start and list training jobs), `GET /v1/gb/training/datasets` (the datasets on the sidecar), `POST /v1/gb/training/datasets` (upload a dataset), `GET /v1/gb/training/dataset-records[?db=]` (the datasets uploaded through the server) and `GET /v1/gb/training/events` (SSE feed of the jobs' progress). The training monitor, which resumes following unfinished jobs after a restart, runs without the admin UI too.
- Admin UI, training view: **upload JSONL training datasets**. Pick a `.jsonl` file (one JSON object per line with `instruction`, `output` and optionally `input`) and press `Upload`; the `Datasets` table lists what was uploaded (status, records, size). The dataset picker lists the uploaded datasets together with the ones on the sidecar, and still shows the uploaded ones when the sidecar cannot be reached.
- Uploading does not involve the sidecar: the Go server validates the file (UTF-8, every line a JSON object with string `instruction`/`output`, plain `*.jsonl` name, at most 64 MB), saves it as a file in a local directory (default `<db path without extension>.training_datasets`, e.g. `bin/gligabao.training_datasets`; `serve --training-dataset-dir`, `ServeOptions.TrainingDatasetDir`) and records it in the new `gb_training_datasets` table (SQLite and PostgreSQL; created automatically). Uploading a file name again replaces the dataset. It works with the sidecar or Modal down.
- A dataset uploaded through the server is sent to the sidecar when a training job that uses it is started, once per version of the file: the record's status goes from `stored` to `synced`, and uploading the name again makes it `stored` again. If the sidecar fails to take it the job is not started (502); if the file is gone from the directory the job answers 500 and asks for a new upload. A dataset name the server has no record of is passed through to the sidecar as it is.

### Changed
- The training endpoints moved out of the admin path: `/v1/gb/admin/api/training/{jobs,datasets,events}` are now `/v1/gb/training/{jobs,datasets,events}`; the old paths are gone. `GET /v1/gb/admin/api/sidecar/status` stays where it is (it is the admin UI's health indicator).
- The training HTTP handlers (jobs, datasets, the progress monitor and its SSE feed) moved from `internal/api/admin` to their own package, `internal/api/training` (`training.StartJobOf`, `training.UploadDatasetOf`, `training.NewMonitor`, `training.Database`, ...). `admin.SidecarStatusOf` takes a `bool` (whether the sidecar is active) instead of the sidecar.

## [0.29.4] - 2026-10-26

### Added
- `run_server_sidecar_local.sh` runs the Python sidecar locally (dev only), and `run_server_local.sh` now starts it first if nothing answers on its health endpoint, and stops it when the server ends. The sidecar needs `MODAL_TOKEN_ID` and `MODAL_TOKEN_SECRET`; without them the server still starts, with a warning.

### Changed
- Docs: `docs/sidecar.md` and the README now say that the Go server only ever talks to the sidecar and the sidecar proxies to Modal (with a diagram), no longer call the sidecar a skeleton, and document the startup health check and `GET /v1/gb/admin/api/sidecar/status`, the Go client's training methods and `StatusError`, that the container copies `routes.py` and `backends/`, that the sidecar needs the Modal tokens (and that `docker-compose.yml` does not pass them yet), and the new steps for adding an endpoint.

## [0.29.3] - 2026-10-26

### Added
- Admin UI: a **sidecar health indicator** in the top bar next to the server's own (`sidecar: up` / `sidecar: down`, refreshed every 15 s). It is a live check made by the server, so it shows when the sidecar goes down or comes back. If the sidecar is up but was down when the server started, it shows `sidecar: up (restart server)`.

### Changed
- The admin endpoint `GET /v1/gb/admin/api/training/status` (0.29.2) is now `GET /v1/gb/admin/api/sidecar/status` and answers `{"sidecar_up": bool, "sidecar_active": bool}`: `sidecar_up` is the live check, `sidecar_active` whether the server uses the sidecar (decided on startup). The training view's notice uses `sidecar_active`.

## [0.29.2] - 2026-10-26

### Added
- On startup the Go server pings the sidecar's `/health` (5 s timeout). If the sidecar is not operating correctly, the server still starts: it logs a warning, sets the `server.SidecarActive` flag to false and runs without a sidecar client, so everything that needs the sidecar answers 503 ("sidecar is not active") or does nothing instead of failing. The check runs once; restart the server after starting the sidecar.
- Admin endpoint `GET /v1/gb/admin/api/training/status` (`{"sidecar_active": bool}`). The admin UI's training view shows a "Sidecar not active" notice and disables `Run` when it is false.

### Changed
- The 503 the training endpoints answer without a sidecar now says "sidecar is not active" (was "not configured").

## [0.29.1] - 2026-10-26

### Added
- The sidecar now serves the training and dataset API itself (`POST /training/job_start`, `GET /training/job_progress`, `GET /training/job_events` (SSE), `POST /infer/query`, `POST /evals/add_to_dataset`, `GET /evals/get_dataset_count`, `POST /datasets/upload`, `GET /datasets/list`; `side_car/routes.py`) and forwards each request to a pluggable **backend** (`side_car/backends/`). The Go server only knows the sidecar API, not what runs behind it. Previously the local sidecar had only `/health`, so the admin UI's dataset list and training calls failed with 404. A backend that cannot be reached gives `502 {"error": ...}`.
- `modal` backend: forwards to the web API of the deployed Modal app (`modal deploy app.py` in `side_car/ai_train`), whose URL it looks up through the Modal SDK on the first request. New settings: `SIDECAR_BACKEND` (default `modal`), `SIDECAR_APP_NAME` (default `gligabao-train`) and `SIDECAR_MODAL_WEB_URL` (skip the lookup, e.g. for `modal serve`).

### Changed
- The Modal credentials (`MODAL_TOKEN_ID`, `MODAL_TOKEN_SECRET`) are required only when the backend is `modal`.
- The sidecar's `requirements.txt` adds `httpx` and `modal`; `Dockerfile.sidecar` copies `routes.py` and `backends/`.

## [0.29.0] - 2026-10-26

### Changed
- The sidecar's settings (`SIDECAR_HOST`, `SIDECAR_PORT`) are now held in one immutable pydantic struct, `SidecarConfig` (`side_car/sidecar_config.py`), loaded once at startup with `SidecarConfig.from_env()` and passed to `create_app(config)` (kept on `app.state.config`). An invalid value (e.g. a non-numeric or out-of-range port) stops the sidecar at startup with a validation error. `Dockerfile.sidecar` copies the new file.
- The sidecar now requires Modal API credentials: `SidecarConfig` has `modal_token_id` and `modal_token_secret` (env `MODAL_TOKEN_ID`, `MODAL_TOKEN_SECRET`), and the sidecar refuses to start if either is missing or empty, instead of failing at the first training call. They are `SecretStr`s, so they do not appear in logs. Pass them to the container with `docker run -e MODAL_TOKEN_ID -e MODAL_TOKEN_SECRET ...`.

## [0.28.0] - 2026-10-26

### Added
- Admin UI **training** view: a `Run` button that starts a Gemma 4 fine-tuning job (model size 4B/12B, number of steps, and the training dataset, picked from the sidecar's datasets), the list of the training jobs in the database, and, for the selected job, a live feed of its events and a graph of its loss. Finished jobs show their stored history.
- Progress goes sidecar → Go → admin UI as Server-Sent Events. The sidecar writes status and progress events for each job (`running`, `training`, `completed` with the final loss, `failed` with the error, and a progress event per Trainer log) and streams them at `GET /training/job_events?job_id=[&from=]`. The Go server follows that stream for every job it starts (and resumes unfinished jobs after a restart), stores the events, keeps the job's row current, and forwards the events at `GET /v1/gb/admin/api/training/events[?db=][&run_id=]`, which replays a job's stored events before going live (`Last-Event-ID` is honoured). A job that never reports its end is marked `failed` 3 hours after it started.
- Admin endpoints `GET /v1/gb/admin/api/training/jobs[?db=][&limit=]` (the jobs, newest first) and `GET /v1/gb/admin/api/training/datasets` (the sidecar's training datasets).
- Training on a chosen dataset: `POST /training/job_start` (and `POST /v1/gb/admin/api/training/jobs`) take a `dataset` (file name on the sidecar's data volume; the sidecar answers 400 for an unknown one, which the admin endpoint passes on as 400). `modal run app.py --action train` takes `--dataset`.
- `gb_training_job_runs` gains `provider_job_id`, `dataset`, `last_step`, `last_loss`, `final_loss`, `error` and `finished_at`; new table `gb_training_job_events` (one row per event of a job). Go: `relational.ListTrainingJobRuns`, `UpdateTrainingJobRun`, `RecordTrainingJobEvent`, `ListTrainingJobEvents`, `NextTrainingJobEventSeq`; `sidecar.Client.StreamTrainingJobEvents`, `sidecar.TrainingEvent` and `sidecar.StatusError`.
- `GET /training/job_progress` takes an optional `job_id`.

### Changed
- `job_id` of a training job is now assigned by the sidecar (it names the job's progress file); the compute provider's id is the new `provider_job_id`. The job's `status` in `gb_training_job_runs` now follows the job (`started`, `running`, `training`, `completed`, `failed`) instead of staying `started`.
- The sidecar's `POST /datasets/upload` now stores the dataset under its own `filename` (a plain `*.jsonl` name; anything else is a 400) instead of always overwriting the default dataset, so several datasets can be kept and picked per job. The default dataset is still used when a job names none.
- The newest progress file (`GET /training/job_progress` without `job_id`) is the most recently modified one.
- `server.NewHandler` takes an `admin.TrainingSidecar` (adds dataset listing and event streaming to `admin.TrainingJobStarter`; `*sidecar.Client` is one) and `admin.StartTrainingJobOf` takes the `*admin.TrainingMonitor`.
- **Schema change**: existing SQLite database files are not migrated; delete the file and let the server recreate it (PostgreSQL: drop `gb_training_job_runs` first; it was added in 0.27.0).

## [0.27.0] - 2026-10-26

### Added
- `TrainingJob` struct on both sides: the sidecar's `POST /training/job_start` now returns it (`job_id`, `status`, `model_size`, `num_train_steps`, `started_at`, a timezone-aware UTC timestamp; `side_car/ai_train/api/models.py`), and the Go sidecar client (`internal/sidecar`) decodes it with the new `Client.StartTrainingJob`. The earlier response fields are unchanged. The sidecar now also rejects a `num_train_steps` that is not a positive integer (400).
- `gb_training_job_runs` table (sqlite and postgres): one row for each training job the sidecar reported as started (job ID, status, model size, steps, when it started, when the row was made). Go: `relational.RecordTrainingJobRun` and `GetTrainingJobRun`.
- Admin endpoint `POST /v1/gb/admin/api/training/jobs[?db=<name>]`, body `{"num_train_steps"?, "model_size"?}` (`"4b"` or `"12b"`; omitted fields take the sidecar's defaults). It calls the sidecar's REST API through the Go client, records the job and answers 201 with the stored row. 400 for a bad body, 502 if the sidecar fails (nothing is recorded), 503 if no sidecar is set. Served with the admin UI.
- Config `sidecar_url` (env `SIDECAR_URL`, default `http://127.0.0.1:8787`): where the sidecar listens.

### Changed
- `server.NewHandler` takes the sidecar client (`admin.TrainingJobStarter`, may be nil) after `providers`.

## [0.26.2] - 2026-10-16

### Changed
- Sidecar HTTP API routes reorganised under prefixes (breaking for direct callers): `POST /training/job_start` and `GET /training/job_progress` (were `/start_training_job`, `/get_training_progress`), `POST /infer/query` (was `/generate_response`), `POST /evals/add_to_dataset` and `GET /evals/get_dataset_count` (were `/add_to_eval_dataset`, `/get_eval_dataset_count`). Docs updated.

### Removed
- The `/upload_dataset` and `/list_datasets` sidecar aliases; use `POST /datasets/upload` and `GET /datasets/list`.

## [0.26.1] - 2026-10-25

### Added
- Sidecar training datasets: a `TrainingDataset` pydantic model (`side_car/ai_train/api/models.py`) used by the dataset endpoints, served at `POST /datasets/upload` and `GET /datasets/list`.
- The Go sidecar client (`internal/sidecar`) gains `UploadTrainingDataset` and `ListTrainingDatasets` and a `TrainingDataset` type, kept separate from the memory `Dataset`.

### Changed
- Dataset timestamps returned by the sidecar (`uploaded_at`, `modified_at`) are now timezone-aware UTC, so the Go client can parse them.

## [0.26.0] - 2026-10-25

### Added
- Gemma-4 fine-tuning and inference in `side_car/ai_train/`, ported from `gloflow_phoenix/gf_phoenix/ai_train`: 4B (`google/gemma-4-E4B-it`, Unsloth, A10G) and 12B (`google/gemma-4-12B-it`, plain transformers + peft + trl, A100, because its `gemma4_unified` architecture cannot be loaded by Unsloth). Each size has its own image (`requirements_train.txt`, `requirements_gemma4_12b.txt`).
- `modal run app.py` actions `sync`, `validate`, `train --model-size 4b|12b` (replaces the stub) and `generate --prompt ...`; `modal serve app.py` serves the web API (`/training/job_start`, `/training/job_progress`, `/infer/query`, `/evals/*` and `/datasets/*` endpoints, `/healthz`).
- Live training progress written to the progress volume, adapters saved per size under `/models/finetuned_model_<size>`, the 325-example tool-calling dataset and its generator in `finetune_data/`, Gemma-4 docs in `docs/training/`, and local unit tests (`python -m unittest test_ai_train_core`).
- Provider interface gains `asgi_app`, `reload_volumes` and per-path `commit_volumes`; new config `SIDECAR_VOL_EVAL` and `SIDECAR_VOL_HF_CACHE`.

### Changed
- `SIDECAR_GPU` is now an optional override; by default 4b uses `a10g` and 12b uses `a100`.
- Fixed in the port: 12B inference now loads through transformers + peft (the original routed it through Unsloth, which cannot load that model).

## [0.25.0] - 2026-10-25

### Added
- Training bootstrap skeleton in `side_car/ai_train/`, modelled on `gloflow_phoenix/gf_phoenix/ai_train`: `app.py` declares the remote functions (`ping`, a stub `train`) and a CLI entrypoint (`modal run app.py [--action ping|train]`); `config.py` holds provider-neutral settings (env overridable: `SIDECAR_PROVIDER`, `SIDECAR_GPU`, `SIDECAR_HF_SECRET`, volume names); `ai_train_core/bootstrap.py` is the code that runs inside the container.
- Compute providers sit behind `providers/base.py` (`Provider`, `FunctionSpec`, `ImageSpec`); Modal is the only implementation (`providers/modal_provider.py`, the only file importing `modal`), so others such as AWS can be added later.
- [docs/sidecar.md](docs/sidecar.md) gains a "Remote training bootstrap" section: layout, how to run it, the `SIDECAR_*` settings, the provider abstraction and how to add a provider.

## [0.24.1] - 2026-10-24

### Added
- `docker-compose.yml` runs the server and the Python sidecar together on one private network (`gligabao`). The server waits for the sidecar's health check, reaches it at `http://sidecar:8787`, keeps its data in the `gligabao-data` volume, and both ports are published on `127.0.0.1` only. `docker compose up --build` starts both (set `ANTHROPIC_API_KEY`, optionally `OPENAI_API_KEY`).

## [0.24.0] - 2026-10-24

### Added
- Python sidecar skeleton in `side_car/`: `main_sidecar.py` is a minimal FastAPI server (`GET /health`) listening on `SIDECAR_HOST:SIDECAR_PORT` (default `127.0.0.1:8787`); dependencies in `side_car/requirements.txt`.
- `Dockerfile.sidecar` packages the sidecar, and `run_build_sidecar.sh [image[:tag]]` builds the image (default `gligabao-sidecar:latest`).
- Go client for the sidecar in `internal/sidecar` (`sidecar.New`, `Client.Health`).
- [docs/sidecar.md](docs/sidecar.md): documents the sidecar, its container and the Go client, linked from the README.

## [0.23.1] - 2026-10-24

### Added
- [docs/hooks.md](docs/hooks.md): documents the hooks available to programs embedding gligabao (`ServeOptions.Hooks` system events with every event type and its fields, and the timeseries provider callback), linked from the README.

## [0.23.0] - 2026-10-15

### Added
- Remember manager. One goroutine now runs every run of the remember text pipeline, **one at a time** (runs are serialized for now). `POST /v1/gb/remember` and `POST /v1/gb/eval/remember` no longer run the pipeline themselves: they send the manager a request, get the ID of the run back at once, and wait for it to complete, so the HTTP behaviour is unchanged. Images are a different pipeline and are still remembered in the handler. See [docs/remember-manager.md](docs/remember-manager.md).
- A run is queued with its row in `gb_remember_runs` already created (status `started`, every stage `not_started`), so its ID is known before it starts and its status can be checked later; the row is overwritten when the run begins. A queued run that is cancelled (the client left) or never runs (shutdown) is marked `failed`.
- The queue is replaceable: by default a Go channel, or callbacks supplied with `memory_remember.WithQueue(QueueHooks{Enqueue, Dequeue})` so that an external queue system can be used. The queue carries run IDs only.
- Go: `memory_remember.NewManager`, `Manager.Submit`, `Manager.Status`, `Manager.Wait`, `Request`, `Outcome`, `QueueHooks`, `WithQueue`, `ErrManagerClosed`; `memory_core.WithRememberRunID`.

### Changed
- Go API: `remember.Handler`, `remember.EvalHandler`, `api.Register` and `api.RegisterEval` take the manager as an argument; `server.NewHandler` takes a context (it bounds the manager) and creates the manager. On shutdown the server stops the manager after the graceful HTTP shutdown.
- A remember request that fails before the pipeline starts, once queued, now leaves a `failed` row in `gb_remember_runs` (before, it left none).

## [0.22.0] - 2026-10-23

### Added
- Admin UI: a **remember runs** view with, per database, the number of runs of the remember pipeline in each status (`started`, `succeeded`, `failed`; click one to filter) and the list of the runs (newest first, paged). Clicking a run shows its parameters and, for each stage, its status, start, duration, model runs and error; a run that succeeded with a failed stage (web search, ...) is flagged in the list. Backed by `GET /v1/gb/admin/api/remember-runs`. See [docs/remember.md](docs/remember.md#admin-ui).

## [0.21.0] - 2026-10-22

### Added
- Remember run log. New table `gb_remember_runs`: one row per execution of the remember pipeline, the authoritative record of how it was executed. It has the `status` (`started`, `succeeded`, `failed`) and `error` of the call as a whole, so a run can be checked without looking at the stages, and for each stage of the pipeline (`classify_complexity`, `chunk_text`, `extract_graph`, `summarize_chunk`, `load_candidates`, `resolve_edges`, `store`, `store_series`, `web_search`, `fetch_series`, `observe_series`) a `<stage>_status` (`not_started`, `started`, `succeeded`, `failed`), an `<stage>_error` (`NULL` if none), `<stage>_started_at` / `<stage>_finished_at` and, for the stages that run a model, `<stage>_model_run_ids`: the IDs of their `gb_model_runs`. The row is written when the call starts and updated at every step. Also stored: the user, dataset, kind, `params` (how the call was made) and `artifact_id`. See [docs/remember.md](docs/remember.md#run-log).
- The `remember` response has `remember_run_id`, the ID of the call's row.
- Go: `relational.CreateRememberRun`, `SaveRememberRun`, `GetRememberRun`, `ListRememberRuns`, `RememberRun`, `RememberStage`, the status constants `RememberNotStarted`, `RememberStarted`, `RememberSucceeded`, `RememberFailed`, and `RememberStages`; In `memory_remember`, the pipeline keeps the log with single calls of `runLog` (`runlog.go`): `j.run.Start(stage)`, `j.run.Done(err, stage)` and `j.run.AddModelRun(stage, id)` for each model run; the calls never fail a call. `Extraction`, `EdgePlan` get a `ModelRunID` for that.
- **Schema change**: a new table; existing database files get it when the server starts (`CREATE TABLE IF NOT EXISTS`).

## [0.20.0] - 2026-10-21

### Added
- Admin UI: a **model runs** view with, per database, the totals of the recorded model runs (runs, tokens, cost), their breakdown by type, stage, model and thinking level, and the list of the runs (newest first, paged, filterable by clicking a breakdown row). Backed by `GET /v1/gb/admin/api/model-runs`. See [docs/model-runs.md](docs/model-runs.md#admin-ui).
- Go: `relational.ModelRunGroups`, `ListModelRuns`, `ModelRunGroup`, `ModelRunFilter`.

## [0.19.0] - 2026-10-20

### Added
- Model runs. New table `gb_model_runs`, the dynamic counterpart of the static `gb_models`: one row per invocation of a model, with `type` (`remember`, `remember_image`, `recall`), `stage` (`extract_graph`, `summarize_chunk`, `resolve_edges`, `resolve_series`, `extract_image`, `web_search`, `classify_complexity`, `embed`, `recall`), `model_id` (→ `gb_models`), `reasoning` (the thinking level it ran with: `none`, `low`, `medium`, `high`), `input_tokens`, `output_tokens`, `cost_usd` and `created_at`. **Every** model call is a run: each LLM call, each embedding request, each web search and each Jev classification. See [docs/model-runs.md](docs/model-runs.md).
- Thinking levels for every stage. `POST /v1/gb/remember`'s `reasoning` now takes any LLM stage (`extract_graph`, `summarize_chunk`, `resolve_edges`, `resolve_series`, `extract_image`), and `none` is kept as given. `POST /v1/gb/recall` takes `reasoning` too. `400` for `web_search` (it does not think) or an unknown level. Thinking is **off by default** (`none`) in both pipelines, so no thinking tokens are paid for unless a request asks for them.
- Eval runner: reasoning levels. A case takes `"reasoning"` (a level per stage, and `"recall"` for the recall's answer) and the runner `-reasoning extract_graph=low,recall=medium`, combined stage by stage like models (the case wins) and recorded in the experiment run's `params`. Every case of the suites now sets all of them explicitly to `none` (checked by a test), as the baseline for comparing levels.
- The chunk summary artifact references its run (`gb_artifacts.model_run_id`); `chunks[].summary` of the `remember` response gets `model_run_id`, and the `summarize_chunk` trajectory step too.
- A model without extended thinking (Claude 3 and 3.5) is called without a thinking budget, and its run records `none` (`ai.SupportsReasoning`, `ai.Response.Reasoning`).
- The Python client's `remember` accepts `reasoning` for any LLM stage and with an image (client version 0.7.0).
- Go: `relational.RecordModelRun`, `GetModelRun`, `ModelRunStats`, `ModelRun`, `ModelRunStat`; `memory_core.RecordRun`, `WithStage`, `StageOf`, `StageRecall`, `DefaultReasoning`, `ResolveRecallReasoning`; `ai.Response.ModelRunID`; `graph.Artifact.ModelRunID`, `graph.NewChunkSummaryArtifact.ModelRunID`.

### Changed
- `gb_llm_calls` is replaced by `gb_model_runs`; `relational.RecordLLMCall`, `LLMCallStats` and `LLMCallStat` are replaced by `RecordModelRun`, `ModelRunStats` and `ModelRunStat`; `memory_core.RecordCall` by `RecordRun`.
- A chunk summary artifact keeps only a reference to its run: `gb_artifacts.model_id` and `gb_artifacts.reasoning` (added in 0.18.0) are replaced by `gb_artifacts.model_run_id` (`graph.Artifact.ModelID` and `Reasoning` are removed; the run holds the model and the level, see `relational.GetModelRun`). `reasoning` is accepted with an `image` (stage `extract_image`).
- **Schema change**: existing database files are not migrated; delete the file and let the server recreate it.

## [0.18.0] - 2026-10-19

### Added
- Chunk summaries. `POST /v1/gb/remember` now also summarizes every chunk of a [chunked](docs/chunking.md#chunk-summaries) text with an LLM call of its own (new stage `summarize_chunk`, choosable in `models`; at most 512 output tokens; made with the extractions, before anything is stored, so a failed summary stores nothing). Each summary is stored as an artifact of its own, of kind `chunk_summary` (only the pipeline creates it), whose parent is the **chunk** (document, chunk, summary), with the session and times of the chunk. No entity or edge is extracted from it. A text that is not chunked has no chunk and so no summary.
- Each summary is embedded into a new vector index of its own, `dataset-<id>-summaries` (vector ID `summary-<artifact id>`, metadata `kind`, `artifact_id`, `chunk_id`, `parent_id`, `index`), in the same embeddings request as the chunk's. `VectorStats` (admin UI **View vec db**) lists it, and `DELETE /v1/gb/datasets/{name}` removes it.
- The model and the reasoning level that wrote each summary are tracked on its artifact: new columns `gb_artifacts.model_id` (→ `gb_models`, the model as the API reports it, registered if new) and `gb_artifacts.reasoning` (`none`, `low`, `medium` or `high`), both NULL for any other artifact.
- Reasoning levels. `POST /v1/gb/remember` takes `reasoning` (`{"summarize_chunk": "low"}`; levels `none`, `low`, `medium`, `high`; `400` for another stage, an unknown level, or with `image`). A level turns on extended thinking for the stage with a budget of 1024, 4096 or 16384 tokens on top of the answer's `max_tokens`. `summarize_chunk` is the only stage that takes one for now.
- The response of a chunked text gets `embedded_summaries` and, per entry of `chunks`, its `summary` (`artifact_id`, `text`, `model`, `reasoning`). Trajectories get a `summarize_chunk` step per chunk, and the `embed` step also embeds the summary.
- Go: `graph.ArtifactChunkSummary`, `graph.NewChunkSummaryArtifact`, `Tx.InsertChunkSummaryArtifact`, `Artifact.ModelID` / `Reasoning`, `Counts.Summaries`, `memory.VectorKindSummary`, `memory.SummaryVectorID`, `memory_core.IndexSummaries`, `memory_core.SummaryDataset`, `memory_core.StageSummarizeChunk`, `memory_core.Reasoning`, `WithReasoning`, `ResolveReasoning`, `ai.Request.Reasoning` and `ai.Reasoning*`, `Result.EmbeddedSummaries`, `ChunkResult.Summary`.
- The Python client's `remember` takes `reasoning` (client version 0.6.0).

### Changed
- `Artifact.IsDerived()` is also true for a chunk summary (so it is not a chunk), and a chunked text now stores two artifacts per chunk, so `Counts.Artifacts` is larger by the number of chunks. A chunked `remember` makes at least one more LLM call per chunk.
- **Schema change** (`gb_artifacts.model_id`, `gb_artifacts.reasoning`): existing database files are not migrated; delete the file and let the server recreate it. Chunks stored before this have no summary.

## [0.17.0] - 2026-10-18

### Added
- Admin UI, timeseries view: each series shows a small line plot (green/red/grey by trend) as a preview. `GET /admin/api/timeseries` returns a `preview` per series: at most 120 points as `[unix ms, value]`, downsampled keeping each bucket's min and max.

## [0.16.0] - 2026-10-15

### Added
- Eval runner: `-suite <name>` runs only the cases of that suite (unknown suite is an error listing the available ones) and `-list-suites` prints the suites with their cases. `run_evals.sh` takes `-s|--suite NAME` and `-h|--help`, which shows a formatted list of the eval suites.

## [0.15.1] - 2026-10-15

### Added
- Eval `timeseries_ingest_aapl` (suite `timeseries.json`): remembers a text with made-up Apple price and volume series and checks that they were stored (points, first/last values, anchors). The eval runner supports `timeseries` in a `remember` entry, `expect_series` in a case, and skips the recall for a case with no `query`.

## [0.15.0] - 2026-10-17

### Added
- Admin UI `timeseries` view, filled in: it lists the timeseries stored in each database (standard and eval), across the datasets of all users, newest update first. Per series it shows the period it covers (first to last point, UTC) and its length in minutes, hours or days (the largest unit that fits; hover for all three), the entities it belongs to (anchored directly, endpoints of an anchored fact, or mentioned by an anchored text), its name, key, field, unit, frequency, source and provider, point count, dataset and owner, last update, the change and min/max/mean of its values, and, folded, its anchors (with role and window) and the fetch definition.
- `GET /v1/gb/admin/api/timeseries?db=<name>[&include_deleted=true]` (admin UI only): the series of all datasets with their stats and labelled anchors, without points.

## [0.14.0] - 2026-10-16

### Added
- Admin UI views. Buttons stacked on the left of the page (always vertically centered on the screen, whatever the height of the view, and fixed while scrolling; their right edge on the left edge of the view) switch between the `main` view (the existing dashboard without evals), a new `evals` view (the evals block moved there from `main`) and a new `timeseries` view (an empty placeholder for now). The chosen view is kept in the URL hash. On narrow screens the buttons move into a row above the view.

## [0.13.0] - 2026-10-16

### Added
- `series_observation` artifacts. `POST /v1/gb/remember` takes `timeseries.observations: true` (with `series`, `fetch` or both; off by default). After the text, the web search and the fetch, `remember` derives a new kind of artifact from each series it stored (supplied or fetched, at most 10 per request): a text generated in Go, not by an LLM, from the series' stats (change, trend, high, low, average, largest move) over the period the request wrote, naming the entity the series is about. That text is extracted, reconciled and stored like any other, so what a series says reaches the graph as bi-temporal facts, deduplicated and embedded like all others. A failure never fails `remember`: it is reported per observation. The response gets `series_observations`. See [docs/timeseries.md](docs/timeseries.md#observations).
- An observation artifact has `kind` `series_observation`, the series it describes (`series_id`), a `query` saying which series and window, and as its parent the artifact that anchors the series (the call's artifact, or for a fetched series the artifact the entity came from). Its `valid_at` is the last point of the window. The provenance chain reads: fact, observation artifact, series, anchoring artifact. Like a web search it is not a chunk.
- Trajectories get the `timeseries_observations` input and an `observe_series` step per series.
- The Python client's `remember` takes `observe_timeseries` (client version 0.5.0).
- Go: `memory_core.WithSeriesObservations`, `Result.SeriesObservations`, `graph.ArtifactSeriesObservation`, `graph.Artifact.SeriesID` and `IsDerived`, `Tx.InsertSeriesObservationArtifact`, `memory_timeseries.ObservationText`.

### Changed
- Database schema: `gb_artifacts` gets a `series_id` column (→ `gb_series`, `ON DELETE SET NULL`). Existing SQLite database files are not migrated: delete the file (and the eval database) and the server recreates it. PostgreSQL adds the column by itself.
- `recall` is not changed: it does not follow observations to their series (on hold, see [docs/timeseries-design.md](docs/timeseries-design.md)).

## [0.12.0] - 2026-10-15

### Added
- Timeseries providers. A program embedding gligabao registers a list of providers when it starts the server (`gligabao.ServeOptions.TimeseriesProviders`). A provider (`gligabao.TimeseriesProvider`) has a `Name`, a natural-language `Description` of what it offers and **in which cases it should be used**, an optional `QueryFormat` (what identifies an entity to it, e.g. a ticker symbol) and a `Fetch` callback with the logic of fetching. The providers are validated at startup.
- `POST /v1/gb/remember` takes `timeseries.fetch` (`providers`, `before`, `after`; all optional; alone or with `series`). After the text is stored, an LLM (new stage `resolve_series`, choosable in `models`) reads the providers' descriptions and decides which provider fetches series for which entity of the text (for example a stock price provider for an entity that is a public company), and what to ask it. The callbacks then run in parallel for the period around the text's reference time (by default 30 days before it and nothing after it, so a memory never holds the future of its event), and the series are stored and anchored to the entity and to the artifact it came from, with the period as the window. Fetching again adds points to the same series. A failure in a provider, the LLM or the storage never fails `remember`: it is reported per fetch. The response gets `series_fetches`. `400` for an unknown provider or a malformed window, `503` if the server has no provider.
- Fetched series have `source` `fetched`, their `provider` and a `definition` (entity and query).
- Trajectories get the `timeseries_fetch` input and the steps `select_series_entities`, `resolve_series_providers` and `fetch_timeseries`.
- The Python client's `remember` takes `fetch_timeseries` (client version 0.4.0).
- Go: `memory_core.SeriesProvider`, `ResolveSeriesProviders`, `WithSeriesProviders`, `WithSeriesFetch`, `ResolveSeriesFetch`, `ParseFetchWindow`; `Result.SeriesFetches`.

### Changed
- `timeseries.fetch` is no longer a `400`; `timeseries` needs `series`, `fetch` or both.
- The stages that take a model (`models`) now include `resolve_series`.
- Go: `api.Register`, `api.RegisterEval` and `server.NewHandler` take the registered providers (nil for none).

## [0.11.0] - 2026-10-14

### Added
- Timeseries. `POST /v1/gb/remember` (and `/v1/gb/eval/remember`) takes an optional `timeseries` with `series`: a series has a `key` (and optional `field`, `name`, `unit`, `frequency`), `points` as `[time, value]` pairs and optional `anchors`. The series are stored next to the text, in the same transaction, and anchored to the artifact the request creates (the default) and to the entities and facts that their anchors select. The entities do not exist when the request is made, so an anchor is a selector (`entity: {name, type}`, `edge: {source, relation, target}`) that is looked up after the text has been extracted and stored; IDs of things already stored (`artifact_id`, `entity_id`, `edge_id`) work too. An anchor can have a `role`, a `window` and an `on_unmatched` (`create`, `ignore`, `error`). A series that cannot be anchored stores nothing (`422`). For a chunked text the series are stored after the chunks, anchored to the parent artifact. The response gets `series`. Not allowed with `image` (`400`); `timeseries.fetch` is reserved and is a `400`. See [docs/timeseries.md](docs/timeseries.md).
- `POST /v1/gb/timeseries` stores series in an existing dataset and anchors them to what it holds, for series that are known only after `remember` returned, that change later, or that are too big for a `remember` request (body up to 16 MiB). Sending a key again adds points to the stored series. `GET /v1/gb/timeseries` lists a dataset's series (stats and anchors, no points) and `GET /v1/gb/timeseries/{id}` reads one with its points between `from` and `to` (and the stats of that window).
- Every series has stats computed in Go whenever points are written: count, first, last, min, max, mean, change, percent change, trend and the largest move between two points.
- New tables `gb_series`, `gb_series_points` and `gb_series_anchors` (SQLite and PostgreSQL). Anchors are real foreign keys, so deleting an artifact, entity or edge removes only its anchors.
- Trajectories get the `timeseries` input and a `store_timeseries` step.
- The Python client's `remember` takes `timeseries`, and has `attach_timeseries` (client version 0.3.0).
- Go: `memory_core.WithSeries`, `ResolveSeries`, `SeriesSpec`; package `memory_timeseries` (`Store`, `Attach`, `Summarize`); `graph.ListSeries`, `GetSeries`, `SeriesPoints`, `ListSeriesAnchors`; new `Tx` methods (`UpsertSeries`, `UpsertSeriesPoints`, `ListSeriesPoints`, `UpdateSeriesSummary`, `AnchorSeries`, `FindEntity`, `FindEdgeID`, `CheckArtifact`); `Result.Series`.
- [docs/timeseries-design.md](docs/timeseries-design.md): the design, the changes made while building the first version, and what is left (fetching series from a data provider, derived observation artifacts, embedding, `recall`).

### Changed
- Database schema: three new tables (see above). Existing database files are not migrated: delete the file (and the eval database) and the server recreates it.
- `remember` returns `422` when a timeseries anchor that must match finds nothing.

## [0.10.0] - 2026-10-14

### Added
- Admin UI: web search. A new **Web search** card shows, per database, the totals of the web searches `remember` made: `web_search` artifacts, the datasets that hold them, sources, entities linked to a result (and those only known from the web), facts created or supported, and, for the eval database, the totals recorded in the eval trajectories (entity searches found / empty / failed, searches made by the search tool, entities and facts extracted, latency, cost). Each dataset with web searches has a **View** button.
- Admin UI: the runs of an experiment show how many web searches each eval made, and an eval that made some gets a **View web searches** button. The list shows, per search, its result, sources, the queries sent to the search engine, the entities and facts extracted from the result, its latency, model, tokens and cost (searches read from a dataset, not from a trajectory, lack the latency, cost and search-engine queries).
- Admin API: `GET /v1/gb/admin/api/web-searches`, `GET /v1/gb/admin/api/web-searches/dataset` and `GET /v1/gb/admin/api/evals/runs/{id}/web-searches`. See [docs/web-search.md](docs/web-search.md#admin-ui).

## [0.9.0] - 2026-10-14

### Added
- Web search for `remember`. `POST /v1/gb/remember` (and `/v1/gb/eval/remember`) takes an optional `web_search` flag (default `false`). When set, after the text is stored the key entities of the text (the extracted entities in the most facts, concepts excluded, at most 3) are searched for on the web, to get the latest world events about each. The search is Anthropic's cloud-based web search tool (`web_search_20250305`), so it is only available when an Anthropic API key is supplied: a request with `web_search` and no key returns `503` (and `400` together with an `image`). The server logs at startup whether web search is available. See [docs/web-search.md](docs/web-search.md).
- Each web search result is an artifact of its own, of the new kind `web_search` (a caller cannot ask for that kind). It saves the query that was issued (new column `gb_artifacts.query`) and its parent (`gb_artifacts.parent_id`) is the artifact from which the searched entity came (for a long text, the chunk). The graph is extracted from the result like from any text (reference time: now), reconciled with the stored facts, and stored: the entities discovered on the web are ordinary nodes, linked to the `web_search` artifact (and to the call's session), embedded when vectors are configured. The searched entities are not searched for in turn.
- The `remember` response gets `web_searches`: per entity searched, its query, the parent artifact, the `web_search` artifact, the sources and what the result yielded (`entities`, `edges`, `invalidated_edges`).
- New model stage `web_search` for `models` (the model that runs the web search tool; the results are extracted on `extract_graph`). The Python client's `remember` takes `web_search`.
- The web search is an enrichment that runs after the text is stored: a failure in it is logged, recorded in the trajectory and reported in the result, and never fails the request. It is billed by Anthropic ($0.01 per search, `ai.WebSearchRequestUSD`) on top of the tokens; that is included in the recorded cost (`gb_llm_calls`, trajectory totals). Trajectories get the steps `select_web_search_entities` and `web_search`.
- Eval: the suite `web_search.json` with one case, `web_search_companies`, that remembers a text about Apple, Microsoft and Nvidia with `web_search`; remember entries of a suite can set `"web_search": true`, and a case `expect_web_searches` (checks on the stored `web_search` artifacts).
- Go: `ai.Client.WebSearch`, `memory_core.WebSearcher`, `WithWebSearcher` (makes it available), `WithWebSearch` (asks for it per call), `ErrWebSearchUnavailable`, `StageWebSearch`, `graph.ArtifactWebSearch`, `Tx.InsertWebSearchArtifact`, `Artifact.Query` and `Artifact.IsChunk`, `Result.WebSearches`.

### Changed
- Database schema: `gb_artifacts` has a new column `query`. Existing database files are not migrated: delete the file (and the eval database) and the server recreates it.
- A child artifact is no longer always a chunk: `Counts.Chunks` counts the artifacts with a `chunk_index`, not those with a `parent_id`.
- Go: `server.NewHandler`, `api.Register` and `api.RegisterEval` take one more argument, the web searcher (nil for none; the server passes the Anthropic client).

## [0.8.0] - 2026-10-14

### Added
- Jev, TypeSafe's "system 1" model, as a small package (`internal/jev`) that wraps calls to its evaluation endpoint (`POST https://api.typesafe.ai/v1/systemone`, model `jev-latest`). It is only used when its API key is supplied: `jev_api_key` (default: the `JEV_API_KEY` environment variable). `jev_base_url` (`JEV_BASE_URL`) and `jev_model` are optional overrides. Without a key the server logs that Jev is off and nothing below runs. A call is one request and is not retried, so the measured latency is that of one request.
- First use, a new first stage of `remember`, `classify_complexity`: Jev answers one `choice` question that classifies the incoming text as `simple` (up to 5 entities), `medium` (6 to 20) or `complex` (more than 20) in the number of entities it is expected to yield, as a prediction of the compute needed per entity and edge. The `confidence` Jev returns with its answer, and the probability of each class, are tracked. For now the result is only logged (`jev: remember text complexity`, with `complexity`, `confidence`, `latency_ms`, text size). The stage is advisory: a Jev failure is logged and never fails `remember`. It always runs on Jev, so it is not part of `models`. At most the first 32 KiB of a text are sent to Jev.
- The stage is a step of the ATIF trajectory of the eval endpoints (`classify_complexity`) with its result (complexity, confidence, probabilities), the Jev model version and tokens, and `latency_ms` / `started_at` in the step's extra (also for a failed call), so Jev latency can be measured and compared across runs.
- Go: `jev.Client` (`Evaluate`, `Classify`), `memory_core.WithJev`, `memory_core.Classifier`, `Trace.TimedTool`.

### Changed
- Go: `server.NewHandler`, `api.Register` and `api.RegisterEval` take one more argument, the Jev classifier (nil for none).

## [0.7.0] - 2026-10-14

### Added
- Per-stage LLM models for `remember`. `POST /v1/gb/remember` (and `/v1/gb/eval/remember`) takes an optional `models` object that picks the model of each LLM-invoking stage: `extract_graph` (entities and edges), `resolve_edges` (duplicate/contradiction check) and `extract_image` (the image pipeline). It can be given with every call. A stage that is left out, or given as an empty string, uses the default model, so a call can change one stage and keep the others; without `models` nothing changes. An unknown stage or a malformed model name returns `400`.
- The model that runs the `extract_graph` stage is the one recorded as the `model_id` of the entities and edges it yields (the model name as the provider reports it), so the stored knowledge reflects the model chosen for that stage.
- Evals can vary the models too: `go run ./evals -models extract_graph=<model>,resolve_edges=<model>` sets them for a whole run (recorded in the experiment run's `params`), and a case of a suite can set `"models"` of its own, which wins stage by stage.
- Go: `memory_core.WithModels`, `memory_core.Models` (`ResolveModels`, `ParseModels`, `Merge`), the `memory_core.Stage*` constants and `CallOptions.ModelLLM`. Eval trajectories record the requested `models` in `extra.input`.
- The Python client's `remember` takes `models`.

## [0.6.1] - 2026-10-14

### Added
- New eval case `chunking_register` (suite `evals/suites/chunking.json`): remembers a ~3,700-word, 60-person register (`evals/suites/corpus/harbor_cooperative.txt`) that needs about 20 chunks, then checks that facts from the start, middle and end were extracted and recalled, and that the chunks are stored correctly (count, size, child artifacts, coverage and overlap of the text, provenance of the facts).
- Eval suites: a `remember` entry can take `text_file` (a file under the suites directory) instead of `text`; a case can set `chunk_size` and `expect_min_chunks` to turn on the chunking checks. See `evals/README.md`.

### Changed
- The eval runner waits up to 30 minutes (was 2) for a `remember` call, since a chunked text is extracted chunk by chunk in one request.

## [0.6.0] - 2026-10-14

### Added
- Chunking of long texts. `POST /v1/gb/remember` with a text of more than one chunk (`chunk_size`, 1024 tokens by default) no longer extracts it in one LLM call: the text is stored as an artifact (the parent), split into overlapping chunks, and every chunk is an artifact too, a child of the parent. See the new `docs/chunking.md`.
- The chunker (`internal/chunk`) sizes chunks in tokens (a pure-Go estimate of a BPE tokenizer by default; `chunk.Tokenizer` can be replaced from Go), cuts at paragraph boundaries (oversized paragraphs fall back to sentences, then words), and repeats the tail of each chunk at the start of the next one (`chunk_overlap_percent` of the chunk size, 20 by default, at most 50).
- Each chunk is processed individually: its own entity/edge extraction, candidate comparison against the facts the earlier chunks stored, storage, and embedding. Entities and edges link to the chunk they came from.
- Chunks are embedded into a dedicated vector dataset, `dataset-<id>-chunks` (vector ID `chunk-<chunk artifact id>`, metadata `kind`, `artifact_id`, `parent_id`, `index`), next to the entity and edge ones, using the same embeddings request as the chunk's entities and edges. It is listed by `VectorStats` (admin UI "View vec db") and removed with the dataset.
- Config: `chunk_size` (default 1024; 32 to 8000) and `chunk_overlap_percent` (default 20; 0 to 50), validated at startup. Go: `memory_core.WithChunking`, `gligabao.Config.ChunkSize` / `ChunkOverlapPercent`.
- The remember response gains `chunks` (artifact ID, index, tokens, overlap, byte range, number of entities and edges extracted per chunk) and `embedded_chunks`, only for a chunked text; `artifact_id` is then the parent's. Eval trajectories gain `chunk_text` and `store_parent_artifact` steps.
- Go: `graph.Tx.InsertChunkArtifact`, `graph.NewChunkArtifact`, `Artifact.ParentID` / `ChunkIndex` / `StartOffset` / `EndOffset` / `Tokens`, `Counts.Chunks`, `memory.ChunkVectorID`, `memory_core.ChunkDataset`, `memory_core.IndexChunks`.

### Changed
- DB schema: `gb_artifacts` gains `parent_id` (self-reference, `ON DELETE CASCADE`), `chunk_index`, `start_offset`, `end_offset` and `tokens`. There is no migration: existing databases (and vector directories) must be recreated.
- A text that is not larger than a chunk is processed exactly as before: no chunks and no chunk vector.
- A chunked text is not one transaction: the chunks go in one after the other (each chunk's candidates must see the previous ones), so a failure while storing chunk *k* leaves the parent and the chunks before it. An extraction failure still stores nothing, because every chunk is extracted before anything is written.
- `graph.Counts.Artifacts` counts chunk artifacts as well; `Counts.Chunks` says how many of them are chunks.
- Go: `graph.Tx` gained `InsertChunkArtifact` (implement it in custom backends); `memory_remember.Result` gained `Chunks` and `EmbeddedChunks`.

## [0.5.0] - 2026-10-14

### Added
- Optional `session_id` on `POST /v1/gb/remember` (text and image) and `POST /v1/gb/recall`: a string the caller chooses (at most 128 bytes, no control characters, else `400`) that labels one user/agent session. In Go it is the option `memory_core.WithSessionID`.
- `remember` attaches the session to the artifact (`gb_artifacts.session_id`, and `gb_image_artifacts.session_id` for images) and echoes it as `session_id` in the response.
- New tables `gb_entity_sessions` and `gb_edge_sessions` record which sessions each entity and each edge (fact) was discovered in. They are written for every entity and edge an artifact of a session mentions or states, including ones that already existed and were therefore not written again, so a session that only repeats known facts is still recorded. A call without a session adds no rows.
- `recall` echoes `session_id`, and sets it as the `session_id` of its eval trajectory (previously always the trajectory's own ID; `remember` trajectories do the same), so the calls of one session can be grouped. It does not change retrieval or the answer, and nothing about the recall is stored. The session ID is also in the `remember.*` / `recall.*` monitoring events and log lines.
- `docs/sessions.md`, and the session fields documented in `remember.md`, `recall.md`, `artifacts.md`, `images.md`, `evals.md` and the README.
- Go: `graph.ListEntitySessions`, `graph.ListEdgeSessions`, `Artifact.SessionID`, `ImageArtifact.SessionID`, `atif.Builder.SetSessionID`.

### Changed
- DB schema: new `session_id` columns and the two link tables. There is no migration: existing databases must be recreated.
- `graph.Tx.InsertArtifact` takes a `sessionID` argument; `Tx` gained `LinkEntitySession` and `LinkEdgeSession`.
- `docs/not-included.md`: the session memory entry now says that the `session_id` label is included and only the per-session cache of turns is left out.

## [0.4.5] - 2026-10-14

### Added
- `docs/not-included.md`: a list of things gligabao deliberately leaves out, starting with session memory (the Cognee-style per-conversation cache), why it is excluded, and the rationale around exact/keyword matching.

## [0.4.4] - 2026-10-14

### Changed
- Refactor, no behaviour change: the text pipeline moved out of `internal/memory` into two new subpackages. `internal/memory/memory_remember` has `Remember`, `Extract`, `Store`, `Result`, `ResolveKind`, `CallRemember` and the fact reconciliation, and `internal/memory/memory_recall` has `Recall`, `RecallResult`, the evidence types and `CallRecall`. What both share moved to `memory_core`: `Fact` (with `FactFromEdge`, `FactFromEvidence`), `Key`, `SynthStatement`, `DatasetID` and `ErrDatasetNotFound` (`memory.ErrDatasetNotFound` is still the same error). Go code that embeds gligabao must use the new import paths (e.g. `memory_remember.Remember`, `memory_recall.Recall`).

## [0.4.3] - 2026-10-14

### Changed
- Refactor, no behaviour change: the image pipeline (`RememberImage`, `LoadImage`, `ExtractImage`, `ImageDir`, `WithImageDir`, ...) moved from `internal/memory` to the new subpackage `internal/memory/memory_image`. What it shares with the text pipeline (the `LLM` interface and call recording, per-call options and the ATIF trace, user and dataset name validation, JSON extraction from LLM output) moved to the new subpackage `internal/memory/memory_core`. Go code that embeds gligabao must use the new import paths (e.g. `memory_core.LLM`, `memory_core.WithEval`, `memory_core.TrajectoryDir`, `memory_image.RememberImage`).

## [0.4.2] - 2026-10-14

### Changed
- Admin UI: the header is narrower and has no background or border; only the title and the health indicator are visible.

## [0.4.1] - 2026-10-14

### Added
- Admin UI: the lower right corner shows, in small text, the running gligabao version and a "github url" link to the commit the binary was built from (only when the binary was built inside a git checkout).
- `GET /version`: the `gligabao` object has a new `commit_url` field (GitHub URL of the full commit).

## [0.4.0] - 2026-10-13

### Added
- Entities and edges now record the model that inferred them: a new `model_id` column of `gb_entities` and `gb_edges`, a reference to the new `gb_models` table (NULL if unknown). It is the model the LLM API reported for the extraction call. An entity shared by several artifacts keeps the model that wrote its stored state: the one that created it, then the one that last replaced its description. The value is set by the pipeline, never taken from the LLM's output.
- New table `gb_models` (`id`, `name`, `provider`, `kind` = `llm` or `embedding`, `supported`, `created_at`). The ID is derived from the name alone (first 8 bytes of the SHA-256 of the trimmed, lowercased name, in hex), so a model has the same ID in every database.
- On every startup gligabao inserts the models it supports into `gb_models` and marks them `supported`: the default chat and embedding models and every key of the price table (built-in prices plus `[prices]` in the config). Models that are no longer supported keep their row with `supported` cleared. A model that is only seen in an API response (e.g. a dated snapshot like `claude-haiku-4-5-20251001`) is inserted, not supported, when something it inferred is stored.
- `model_id` appears in the graph JSON (`GET /v1/gb/admin/api/graph`, the eval graph endpoint), which also lists the referenced models (`models`: id, name, provider, kind, supported), in recall evidence and in the remember result.
- Admin UI (eval graph view): the model name is shown in the tooltips, in the details of the clicked entity and of each of its facts, and the status line lists the models that inferred the graph.

### Changed
- DB schema: new `gb_models` table and `model_id` columns. There is no migration: existing databases must be recreated.

## [0.3.0] - 2026-10-13

### Added
- Admin UI: paste an image (Ctrl+V) anywhere on the page to remember it. The image opens in an overlay over a tinted background; enter the user and dataset and press Remember to call `POST /v1/gb/remember` with the image. A spinning circle shows while the server works, and the extracted metadata (theme, title, text blocks, hashes, model, tokens, cost, cached or new) appears next to the image. Esc, the Close button or a click on the background closes it.
- Admin UI: an "Image store" card on the dashboard with the number and total size of the stored images, their extractions, LLM tokens and cost, counts by media type and by theme, and a comparison of the image directory with the database (files on disk, missing files, untracked files). It refreshes after an image is remembered from the UI.
- `GET /v1/gb/admin/api/images[?db=<name>]`: the statistics behind the card.

## [0.2.0] - 2026-10-13

### Added
- Image knowledge extraction: `POST /v1/gb/remember` (and the eval endpoint) accepts a base64 `image` (JPEG, PNG, GIF or WebP, up to 5 MiB) instead of `text`. It runs a separate image pipeline: one vision-LLM call (default model `claude-haiku-5-5`) reads the title and text blocks in the image (OCR) and classifies the whole image by a theme (`plot`, `chart`, `graph`, `diagram`, `table`, `document`, `screenshot`, `slide`, `map`, `photo`, `illustration`, `other`). See [docs/images.md](docs/images.md).
- Image files are stored on the file system, never in the database: in an image directory next to the SQLite file (`<name>.images`, e.g. `bin/gligabao.images`) as `<dataset id>/<sha256>.<ext>`. Set it with `serve --image-dir` or `ServeOptions.ImageDir`.
- New tables, created on startup in existing databases too: `gb_image_artifacts` (metadata only: media type, SHA-256, size and the file's path relative to the image directory), `gb_image_extractions` (theme, title, text, the model's raw output, tokens and cost) and `gb_image_text_blocks` (the text regions in reading order). Nothing is added to the graph, the vector store or `gb_artifacts` yet; grounding comes later.
- An image already in the dataset (same SHA-256) is not processed again: `remember` returns the stored extraction with `"cached": true`, without an LLM call or new rows. Only the same user's dataset is searched.
- Image calls are logged in `gb_llm_calls` with type `remember_image`, and emit the `remember_image.completed` / `remember_image.failed` system events.
- The LLM client can send images (`ai.Message.Images`).
- Python client: `Client.remember(user_id, image=<bytes or path>)`; `text` is now optional (exactly one of `text` and `image` is required).

### Changed
- A text larger than 1 MiB is still rejected with `400`, but the request body limit is now about 7 MiB, to fit images.

## [0.1.0] - 2026-10-13

First tracked version; summarizes everything built so far.

### Added
- Temporal knowledge graph memory: `POST /v1/gb/remember` extracts entities and facts (with validity intervals and artifact provenance) from text; `POST /v1/gb/recall` answers questions with evidence.
- Datasets owned by users, OWL ontologies, graph export (`GET /v1/gb/datasets/{name}/graph`).
- SQLite and PostgreSQL relational backends; chromem-go (default) and Pinecone vector stores; entities and edges embedded into per-dataset vector stores.
- Public Go API at the repo root for embedding gligabao, and the `gligabao` CLI (`serve`).
- Eval mode: endpoints returning ATIF trajectories, eval suites, experiments and runs kept in an eval database.
- Embedded admin UI at `/v1/gb/admin/`: datasets, graphs, vectors, evals and remember traces.
- System event hooks (`monitoring`), structured logging with logrus.
- Version tracking: `VERSION` embedded in the binary, this changelog, `gligabao version`, `--version`, `GET /version`.
