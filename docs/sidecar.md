# Python sidecar

`internal/api/training/`, `sidecar/`, `sidecar/routes.py`, `sidecar/backends/`, `sidecar/ai_train/`, `Dockerfile.sidecar`, `docker-compose.yml`, `run_build_sidecar.sh`, `internal/sidecar/`

The sidecar is a small Python service that runs next to the Go server and that the server talks to over HTTP. It exists for work that is easier to do in Python (libraries that have no good Go equivalent). **The Go server only ever talks to the sidecar, and the sidecar proxies to the compute backend (Modal today).** The Go code never calls Modal or any other provider, never holds provider credentials and never learns what runs behind the sidecar's REST API.

```
Go server ── HTTP ──► sidecar (sidecar/, FastAPI) ── HTTP ──► backend: web API of the Modal app (sidecar/ai_train)
internal/sidecar        routes.py → Backend.forward              runs the jobs on a cloud GPU
```

The sidecar answers a health check itself and forwards the training/dataset API, unchanged, to its backend. Behind the `modal` backend sits the bootstrap for remote (cloud GPU) training jobs; see [Remote training bootstrap](#remote-training-bootstrap).

## The service

`sidecar/main_sidecar.py` is a [FastAPI](https://fastapi.tiangolo.com/) app run by [uvicorn](https://www.uvicorn.org/). Dependencies are in `sidecar/requirements.txt`.

| Endpoint | Response |
|---|---|
| `GET /health` | `200 {"status": "ok"}` (answered by the sidecar itself: it says the sidecar is up, not that the backend is reachable) |
| `POST /training/job_start`, `GET /training/job_progress`, `GET /training/job_events` (SSE), `POST /infer/query`, `POST /evals/add_to_dataset`, `GET /evals/get_dataset_count` | Answered by the **backend** (below) |

### Backends

The routes in `sidecar/routes.py` are the only API the Go server knows (`internal/sidecar/`); it never talks to Modal or any other provider. Each route is forwarded unchanged (method, query, body; status and body come back as they are, SSE streamed) to the configured `Backend` (`sidecar/backends/`). A `4xx`/`5xx` from the backend reaches the Go server unchanged; if the backend cannot be reached the sidecar answers `502 {"error": ...}` (for `modal`, also when the deployed app's web URL cannot be found). To add a backend, implement `Backend.forward` in `backends/` and register it in `backends/__init__.py::get_backend`; an HTTP service that serves the same API can subclass `HttpBackend` and only provide its URL.

The only backend today is `modal`: the web API (`serve_api`) of the Modal app in `sidecar/ai_train/app.py`. Deploy it with `cd sidecar/ai_train && modal deploy app.py`; the sidecar finds its URL through the Modal SDK (once, on the first request). With `modal serve` (dev), set `SIDECAR_MODAL_WEB_URL` to the URL it prints.

Configuration is by environment variable. At startup `main_sidecar.py` reads them once into a `SidecarConfig` (a frozen pydantic model, `sidecar/sidecar_config.py`) and passes it to `create_app(config)`; the app keeps it on `app.state.config` (handlers can use the `get_config` helper). New settings are added as fields of that struct. An invalid value stops the sidecar at startup.

| Variable | Default | Meaning |
|---|---|---|
| `SIDECAR_HOST` | `127.0.0.1` | Interface to listen on. Local only by default, as the sidecar has no authentication. |
| `SIDECAR_PORT` | `8787` | Port to listen on. |
| `SIDECAR_BACKEND` | `modal` | Which backend answers the API. |
| `SIDECAR_APP_NAME` | `gligabao-train` | Modal backend: name of the deployed Modal app. |
| `SIDECAR_MODAL_WEB_URL` | *(unset)* | Modal backend: use this URL instead of looking the app up. |
| `MODAL_TOKEN_ID` | *(required for `modal`)* | Modal API token id. |
| `MODAL_TOKEN_SECRET` | *(required for `modal`)* | Modal API token secret. |

With the `modal` backend the Modal credentials are checked when the config is loaded: the sidecar refuses to start if either is missing or empty (they are held as `SecretStr`, so they do not show up in logs). Create a token with `modal token new`; a `~/.modal.toml` is not read by this check.

Run it without a container:

```sh
cd sidecar
python3 -m venv .venv && . .venv/bin/activate
pip install -r requirements.txt
export MODAL_TOKEN_ID=ak-... MODAL_TOKEN_SECRET=as-...
python main_sidecar.py              # listens on 127.0.0.1:8787
curl http://127.0.0.1:8787/health
```

## Remote training bootstrap

`sidecar/ai_train/` fine-tunes Gemma-4 (4B and 12B, LoRA) on a cloud provider and serves the result. It is ported from `gloflow_phoenix/gf_phoenix/ai_train` and restructured so the cloud-specific parts sit behind a provider interface. It is the Modal side of the `modal` backend: it runs on Modal, is not imported by `main_sidecar.py`, and is not copied into `Dockerfile.sidecar`.

```
sidecar/ai_train/
  app.py                      # declares the remote functions + CLI entrypoint (provider-neutral)
  config.py                   # TrainConfig, overridable by env vars
  training_data.py            # local path of the dataset
  requirements_train.txt      # 4B image: Unsloth stack
  requirements_gemma4_12b.txt # 12B image: plain transformers + peft + trl (no Unsloth)
  requirements_web.txt        # web API image
  finetune_data/              # generate_dataset.py + the 325-example tool-calling dataset
  (training docs moved to top-level docs/training/)
  test_ai_train_core.py       # local unit tests (no GPU / Modal needed)
  ai_train_core/              # runs inside the container; never imports a provider SDK
    model_config.py           # 4b / 12b settings (pure data)
    paths.py                  # container mount points + file locations
    prompt_format.py          # Gemma turn-marker format shared by training and inference
    dataset.py                # sync / validate the dataset on the data volume
    training_common.py        # dataset loading, SFTConfig, adapter saving
    progress.py               # Trainer callback writing live progress events
    progress_events.py        # the progress file format: write/read events (no heavy imports)
    model_load.py, model_train.py            # 4B via Unsloth
    model_train_gemma4_12b.py                # 12B without Unsloth (training + inference loading)
    inference.py              # generate with the trained adapter
    bootstrap.py              # ping + train dispatch by backend
  api/
    api.py                    # FastAPI app (training jobs, progress, generate, eval, datasets)
  providers/
    base.py                   # Provider (ABC), FunctionSpec, ImageSpec
    modal_provider.py         # Modal implementation; the only file that imports modal
    __init__.py               # get_provider(name)
```

### Why two training paths

`google/gemma-4-E4B-it` (4B) uses the `gemma4` architecture, which Unsloth supports. `google/gemma-4-12B-it` uses the newer `gemma4_unified` architecture, which needs `transformers>=5.10.1`; Unsloth pins `transformers<=5.5.0` and has no loader for it. So 12B trains and serves through plain transformers + peft + trl in a separate image, and 4B stays on Unsloth. Both save LoRA adapters to `/models/finetuned_model_<size>`. Details in the `model_train_gemma4_12b.py` docstring and `docs/training/MODEL_SIZES.md`.

### Running it (Modal)

Needs the `modal` CLI, logged in (`pip install modal && modal setup`), and a provider secret holding `HF_TOKEN` for the Gemma weights (`SIDECAR_HF_SECRET`).

```sh
cd sidecar/ai_train
modal run app.py                                   # ping: image, volumes, GPU info, whether HF_TOKEN is set
modal run app.py --action sync                     # upload finetune_data/*.jsonl to the data volume (only if newer)
modal run app.py --action validate                 # check the remote dataset
modal run app.py --action train --train-steps 100 --model-size 4b     # 4b on A10G, 12b on A100
modal run app.py --action generate --model-size 4b --prompt "..."     # run the fine-tuned model
modal serve app.py                                 # web API in dev mode (`modal deploy app.py` to persist)
```

`train` syncs the dataset first; `--dataset <file>` trains on another file of the data volume instead of the default one. Web API endpoints: `POST /training/job_start`, `GET /training/job_progress`, `GET /training/job_events` (SSE), `POST /infer/query`, `POST /evals/add_to_dataset`, `GET /evals/get_dataset_count`, `GET /healthz`.

`POST /training/job_start` (body `{num_train_steps?, model_size?, dataset?, dataset_content?}`) answers with a `TrainingJob` (`job_id`, `provider_job_id`, `status`, `model_size`, `num_train_steps`, `dataset`, `started_at`). `job_id` is assigned by the sidecar and names the job's progress file; `provider_job_id` is the compute provider's id of the spawned call. `dataset` is the file name of a `*.jsonl` on the data volume (a plain name, else 400); omitted means the default dataset. The sidecar has **no dataset endpoints**: datasets are owned by the Go server. When the Go server starts a job on one of its datasets it sends the file inline as `dataset_content` (base64); the sidecar stores it on the data volume as `dataset` (replacing an earlier file of that name) before it spawns the job. Without `dataset_content`, `dataset` must already be on the volume (else 400). `dataset_content` without `dataset`, or not valid base64, is a 400. Inside the sidecar a dataset is passed around as the pydantic `TrainingDataset` (`api/models.py`: `filename`, `content` (base64, never serialized), `size_bytes`, `uploaded_at`, `modified_at`; `payload()`, `from_path()`).

### Training progress, end to end

1. The training container writes **events** to `/progress/training_progress_<job_id>.jsonl` (`ai_train_core/progress_events.py`): `{"type":"status","status":"running"|"training"|"completed"|"failed", ...}` (`completed` carries `final_loss`, `failed` the `error`; non-finite numbers are sent as `null`) and `{"type":"progress","step","loss","learning_rate","epoch","elapsed_seconds"}` for each Trainer log.
2. `GET /training/job_events?job_id=<id>[&from=<n>]` streams that file as **SSE**: `id: <event index>`, `event: status|progress`, `data: <event JSON>`, with `: keepalive` comments while idle. It ends after `completed`/`failed`, or after a few minutes (the reader reconnects with `from=<last id + 1>`).
3. The Go server follows that stream for every job started through the training API (`internal/api/training/monitor.go`): it stores each event in `gb_training_job_events`, keeps the job's `gb_training_job_runs` row current (`status`, `last_step`, `last_loss`, `final_loss`, `error`, `finished_at`), and forwards the events to its SSE feed (`/v1/gb/training/events`). After a restart it resumes the jobs that are still unfinished from the last stored event. If the sidecar never reports an end within 3 hours of the job's start, the job is marked `failed`.
4. `GET /v1/gb/training/events[?db=<name>][&run_id=<id>]` is the SSE feed (the admin UI uses it) (`event: status|progress`, `data: {db, run_id, job_id, seq, type, ...}`). With `run_id` it first replays the job's stored events (resuming after `Last-Event-ID`), then goes live; without it, it is the live events of all jobs.

### Admin UI: the training view

The **training** view of the [admin UI](../README.md) has a dataset upload (a file input and the table of uploaded datasets), a `Run` button (model size 4B/12B, dataset, steps), the list of the jobs in `gb_training_job_runs`, and for the selected job the event feed and a graph of its loss. The training endpoints are part of the API, under `/v1/gb/training/` (served whether or not the admin UI is on); the sidecar status stays with the admin UI:

| Endpoint | |
|---|---|
| `GET /v1/gb/admin/api/sidecar/status` | `{"sidecar_up": bool, "sidecar_active": bool}`. `sidecar_up` is a live health check of the sidecar (the admin UI's `sidecar:` badge in the top bar, refreshed every 15 s); `sidecar_active` is false if it failed its health check on startup (the training view then shows a "Sidecar not active" notice and disables `Run`; only a server restart changes it). |
| `POST /v1/gb/training/jobs[?db=]` | body `{"num_train_steps"?, "model_size"?, "dataset"?}`; starts a job, answers 201 with the stored row. 400 for a bad body or a dataset the sidecar rejects, 502 if the sidecar fails, 503 if the sidecar is not active (it failed its health check on startup). |
| `GET /v1/gb/training/jobs[?db=][&limit=]` | `{"jobs": [...]}`, newest first (default 50, at most 500). |
| `POST /v1/gb/training/datasets[?db=]` | uploads a JSONL training dataset: multipart form, the file in the field `file` (optionally `filename`). **The sidecar is not involved.** The server validates it (UTF-8, each non-blank line a JSON object with string `instruction` and `output`, optional string `input`; plain `*.jsonl` name; at most 64 MB), saves it in the training dataset directory (`serve --training-dataset-dir`, default `<db path without extension>.training_datasets`) and records it in `gb_training_datasets` (re-uploading a name replaces it). Answers 201 with the record; 400 for an invalid file, 413 if too large. |
| `GET /v1/gb/training/datasets[?db=]` | `{"datasets": [...]}`: the training datasets (read from the database only; the sidecar is not asked) (`filename`, `size_bytes`, `num_records`, `sha256`, `status` `stored`/`synced`, `updated_at`, `synced_at`), latest first. |
| `GET /v1/gb/training/events[?db=][&run_id=]` | the SSE feed above. |

A dataset lives on the Go server only (file + `gb_training_datasets` record). When a job is started on it, the server sends the file to the sidecar inside the `POST /training/job_start` request (`dataset_content`) and marks the record `synced` once the sidecar has taken it; if the sidecar fails the job is not started (502). A dataset name the server has no record of is refused with 400; a job without a dataset uses the sidecar's default dataset.

The sidecar's address is `sidecar_url` / `SIDECAR_URL` (default `http://127.0.0.1:8787`).

Local tests: `cd sidecar/ai_train && python -m unittest test_ai_train_core -v`.

### Configuration

All settings are in `config.py` and can be overridden by environment variable:

| Variable | Default | Meaning |
|---|---|---|
| `SIDECAR_PROVIDER` | `modal` | Compute provider (only `modal` exists). |
| `SIDECAR_APP_NAME` | `gligabao-train` | Name of the provider app. |
| `SIDECAR_GPU` | *(empty)* | GPU override for train/generate of every model size (Modal names, e.g. `a100`). Empty means the per-size default: `a10g` for 4b, `a100` for 12b. |
| `SIDECAR_TRAIN_TIMEOUT_S` | `7200` | Timeout of the train jobs. |
| `SIDECAR_HF_SECRET` | *(empty)* | Name of a provider secret with `HF_TOKEN`. Empty means no secret is attached. |
| `SIDECAR_VOL_DATA` | `gligabao-train-data` | Volume mounted at `/data` (datasets). |
| `SIDECAR_VOL_MODELS` | `gligabao-train-models` | Volume mounted at `/models` (fine-tuned adapters). |
| `SIDECAR_VOL_PROGRESS` | `gligabao-train-progress` | Volume mounted at `/progress` (training progress). |
| `SIDECAR_VOL_EVAL` | `gligabao-train-eval` | Volume mounted at `/eval` (eval examples from the web API). |
| `SIDECAR_VOL_HF_CACHE` | `gligabao-train-hf-cache` | Volume mounted at `/root/.cache/huggingface` (base-model cache, avoids re-downloads). |

Volumes are created if missing.

### How the provider abstraction works

`app.py` only says *what* to run. It builds a `FunctionSpec` (image, GPU, timeout, volumes, secrets) and registers functions with `@provider.function(spec)` (or `@provider.asgi_app(spec)` for the web API). It runs them with `provider.call(fn, ...)` or `provider.spawn(fn, ...)` and persists/refreshes volumes with `provider.commit_volumes(paths)` / `provider.reload_volumes(paths)`. Images are described by a provider-neutral `ImageSpec` (CUDA base or not, requirements file); the provider decides how to build them, and mounts the `ai_train/` directory into the container at `/pkg`. Code in `ai_train_core/` and `api/` gets what it needs from the provider as injected callables (e.g. `commit_fn`).

Rules that keep it swappable:

- Nothing outside `providers/` imports a provider SDK, and `ai_train_core/` / `api/` (the code that runs in the container) never do.
- `app = provider.native_app` is a module-level object because provider CLIs such as `modal run` look for it.
- Code that runs remotely imports `ai_train_core` inside the function body, since it only exists in the container's `/pkg` mount, and the heavy ML imports are per image (the 12B image has no Unsloth).

To add a provider (e.g. AWS): implement `Provider` in `providers/aws_provider.py` and add one branch to `get_provider` in `providers/__init__.py`. If the new provider cannot express something in `FunctionSpec`, extend `FunctionSpec` rather than leaking provider details into `app.py`.

### Growing it

1. Add a dependency: put it in the requirements file of the image that needs it (an image is rebuilt only when its file changes; code changes do not rebuild it).
2. Add a model size: an entry in `ai_train_core/model_config.py`, a train/generate function pair in `app.py`, and a pipeline module if it needs a new backend.
3. Add another remote function: declare it in `app.py` with a `FunctionSpec` and a matching action in `main`.

## The container

`Dockerfile.sidecar` (in the repo root; the plain `Dockerfile` is the Go server's) packages the sidecar on `python:3.13-slim`: it installs the requirements, copies `main_sidecar.py`, `sidecar_config.py`, `routes.py` and `backends/`, runs as the non-root user `sidecar` and starts `python main_sidecar.py`. The image sets `SIDECAR_HOST=0.0.0.0` (a container must listen on all interfaces for a published port to reach it) and exposes `8787`.

```sh
./run_build_sidecar.sh                         # builds gligabao-sidecar:latest
./run_build_sidecar.sh myrepo/sidecar:dev      # or a name of your choice
docker run --rm -p 127.0.0.1:8787:8787 -e MODAL_TOKEN_ID -e MODAL_TOKEN_SECRET gligabao-sidecar:latest
```

Without the two Modal variables the container exits at startup. Publish the port on `127.0.0.1` (as above) unless the sidecar must be reachable from other machines: it has no authentication.

### With docker compose

`docker-compose.yml` starts the server and the sidecar on the same network (`gligabao`):

```sh
export ANTHROPIC_API_KEY=...
docker compose up --build
```

On that network the server reaches the sidecar at `http://sidecar:8787` (the service name; `127.0.0.1` would be the server's own container). The server starts only once the sidecar's `/health` check passes. Both ports are published on `127.0.0.1` only; the server's data lives in the `gligabao-data` volume. The sidecar needs `MODAL_TOKEN_ID` and `MODAL_TOKEN_SECRET` in its environment; `docker-compose.yml` does not pass them yet, so add them under the `sidecar` service's `environment:` (the server needs none).

## The Go client

`internal/sidecar` is the only package in the Go code that knows the sidecar's HTTP API, and the sidecar is the only thing the Go server calls for this work: there is no Modal client, token or URL anywhere in Go. `sidecar.go` has the client and `Health`; `training_datasets.go`, `training_jobs.go` and `training_events.go` the training calls.

```go
c := sidecar.New("")                       // "" is sidecar.DefaultBaseURL, http://127.0.0.1:8787
resp, err := c.Health(ctx)                 // resp.Status == "ok"
```

- `sidecar.New(baseURL)` returns a `*Client` with a 30 second timeout per request. A trailing `/` on the URL is ignored. The address is the `sidecar_url` / `SIDECAR_URL` setting.
- `Client.Health(ctx)` calls `GET /health`. It returns an error if the sidecar cannot be reached, answers with a status other than 200, sends a body that is not the expected JSON, or reports a `status` other than `"ok"`.
- `Client.StartTrainingJob`, `Client.StreamTrainingJobEvents` (SSE), call `POST /training/job_start` (`StartTrainingJobRequest.DatasetContent` carries the dataset file) and `GET /training/job_events`. There are no dataset methods: the sidecar has no dataset endpoints.
- Errors name the method and path (`sidecar GET /health: ...`) and, for a bad status, include the start of the response body. A bad status is a `*sidecar.StatusError` (`Code`, `Body`, `Message()`), so callers can tell a request the backend rejected (400) from a sidecar that is down.

### Startup check and status

On startup the Go server calls `GET /health` once (5 second timeout). If the sidecar does not answer correctly the server still starts: it logs a warning, sets the `server.SidecarActive` flag to false and runs without a sidecar client, so everything that needs the sidecar answers `503` ("sidecar is not active") or does nothing. The check runs once, so restart the server after starting the sidecar. A sidecar whose backend (Modal) is down still counts as active; its calls then fail with `502` when they are made.

`GET /v1/gb/admin/api/sidecar/status` answers `{"sidecar_up": bool, "sidecar_active": bool}` (see the table above); the admin UI shows `sidecar_up` as a badge in the top bar.

The tests (`internal/sidecar/*_test.go`) run the client against an `httptest` server, so they need neither Python nor Docker.

## Adding an endpoint

1. Add the path to `ROUTES` in `sidecar/routes.py`. It is then forwarded to the backend as it is.
2. Implement it in the backend: for `modal`, a route of the web API in `sidecar/ai_train/api/`. Another backend must serve the same path.
3. Add a method to `Client` in `internal/sidecar` (a new file per topic, like `training_jobs.go`), with typed request and response structs. Keep all calls to the sidecar in that package, and never call a backend from Go.
4. If the Go server should start it, add an admin handler in `internal/api/admin` that takes the client through a small interface.
5. A route answered by the sidecar itself, like `/health`, goes in `sidecar/main_sidecar.py` instead (and any new package in `sidecar/requirements.txt`). If the Dockerfile must copy new files, update `Dockerfile.sidecar`.
6. Document the endpoint in the tables above.
