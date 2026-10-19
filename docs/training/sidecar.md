# Python sidecar

`internal/api/training/`, `sidecar/`, `sidecar/routes.py`, `sidecar/backends/`, `sidecar/ai_train/`, `Dockerfile.sidecar`, `docker-compose.yml`, `run_build_sidecar.sh`, `internal/sidecar/`

The sidecar is a small Python service that runs next to the Go server and that the server talks to over HTTP. It exists for work that is easier to do in Python (libraries that have no good Go equivalent). **The Go server only ever talks to the sidecar, and the sidecar proxies to the compute backend (Modal today).** The Go code never calls Modal or any other provider, never holds provider credentials and never learns what runs behind the sidecar's REST API.

```
Go server ── HTTP ──► sidecar (sidecar/, FastAPI) ── HTTP ──► backend: web API of the Modal app (sidecar/ai_train)
internal/sidecar        routes.py → Backend.forward              runs the jobs on a cloud GPU
```

The sidecar answers a health check itself and forwards the training/dataset API, unchanged, to its backend. Behind the `modal` backend sits the bootstrap for remote (cloud GPU) training jobs; see [Remote training bootstrap](#remote-training-bootstrap).

## The service

`sidecar/main_sidecar.py` is a [FastAPI](https://fastapi.tiangolo.com/) app run by [uvicorn](https://www.uvicorn.org/). Dependencies are in `sidecar/requirements_sidecar.txt`.

| Endpoint | Response |
|---|---|
| `GET /health` | `200 {"status": "ok"}` (answered by the sidecar itself: it says the sidecar is up, not that the backend is reachable) |
| `POST /training/job_start`, `GET /training/job_progress`, `GET /training/job_events` (SSE), `POST /training/job_cancel`, `POST /infer/query`, `POST /evals/add_to_dataset`, `GET /evals/get_dataset_count` | Answered by the **backend** (below) |

### Backends

The routes in `sidecar/routes.py` are the only API the Go server knows (`internal/sidecar/`); it never talks to Modal or any other provider. Each route is forwarded unchanged (method, query, body; status and body come back as they are, SSE streamed) to the configured `Backend` (`sidecar/backends/`). A `4xx`/`5xx` from the backend reaches the Go server unchanged; if the backend cannot be reached the sidecar answers `502 {"error": ...}` (for `modal`, also when the deployed app's web URL cannot be found). To add a backend, implement `Backend.forward` in `backends/` and register it in `backends/__init__.py::get_backend`; an HTTP service that serves the same API can subclass `HttpBackend` and only provide its URL.

The only backend today is `modal`: the web API (`serve_api`) of the Modal app in `sidecar/ai_train/app.py`. Deploy it from the admin UI ([modal.md](modal.md)) or with `cd sidecar/ai_train && modal deploy app.py`; the sidecar finds its URL through the Modal SDK (once, on the first request). With `modal serve` (dev), set `SIDECAR_MODAL_WEB_URL` to the URL it prints.

Backend actions: the sidecar also answers a few routes itself, for what an operator does to a backend (`backends/actions.py`): `GET /backends` (live status), `POST|GET /backends/{name}/actions/{action}`, `.../events` (SSE), `POST .../cancel`, `GET /backends/{name}/volumes` and `GET /backends/{name}/apps` (the active apps of this system, each with the same action routes under `/backends/{name}/apps/{app}/actions/...`). `modal` uses them to manage the Modal app's lifecycle without the `modal` CLI: `deploy` (`ai_train/providers/modal/deploy.py`, the SDK's `app.deploy()`), `stop` (`ai_train/providers/modal/stop.py`, `modal app stop`; it ends running jobs too) the volume list (`ai_train/providers/modal/list_volumes.py`, read only) and the list of the active apps (`ai_train/providers/modal/list_apps.py`, read only; the configured app and `<it>-<suffix>` ones). The Go server passes them on as `/v1/gb/admin/api/sidecar/backends[/{name}/actions/{action}[/events|/cancel]]` and `.../{name}/volumes`; the admin UI's training view has a list of the apps, each with Redeploy, Last log, Stop and Volumes, and a live log dialog. Details, API and troubleshooting: [modal.md](modal.md).

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
pip install -r requirements_sidecar.txt
export MODAL_TOKEN_ID=ak-... MODAL_TOKEN_SECRET=as-...
python main_sidecar.py              # listens on 127.0.0.1:8787
curl http://127.0.0.1:8787/health
```

## Remote training bootstrap

`sidecar/ai_train/` fine-tunes Gemma-4 (4B and 12B, LoRA) on a cloud provider and serves the result. It is ported from `gloflow_phoenix/gf_phoenix/ai_train` and restructured so the cloud-specific parts sit behind a provider interface. It is the Modal side of the `modal` backend: it runs on Modal and is not imported by `main_sidecar.py`; `Dockerfile.sidecar` copies it only so that the sidecar can deploy it (see [modal.md](modal.md)).

```
sidecar/ai_train/
  app.py                      # declares the remote functions + CLI entrypoint (provider-neutral)
  config.py                   # TrainConfig, overridable by env vars
  training_data.py            # the local datasets (bin/datasets_training/*/dataset.toml), for the CLI
  requirements_train.txt      # the training image (Unsloth; 4B and 12B)
  requirements_infer.txt      # the inference-only image (transformers + peft + bitsandbytes; no Unsloth)
  requirements_web.txt        # web API image
  (training docs moved to top-level docs/training/)
  test_ai_train_core.py       # local unit tests (no GPU / Modal needed)
  ai_train_core/              # runs inside the container; never imports a provider SDK
    model_families.py         # registry of model families; family-neutral code looks the family's modules up here
    paths.py                  # container mount points + file locations
    dataset.py                # store / sync / validate a dataset on the data volume
    training_common.py        # dataset loading, SFTConfig, adapter saving
    progress.py               # Trainer callback writing live progress events
    progress_events.py        # the events of a job: write helpers, stop flag (no heavy imports)
    event_log.py              # the job's event log on the provider's key-value store (Modal Dict)
    gemma4/                   # everything Gemma-4 specific (the contract of a family: gemma4/__init__.py)
      model_config.py         #   4b / 12b settings (pure data)
      prompt_format.py        #   Gemma turn-marker format shared by training and inference
      model_load.py, model_train.py   # training: 4B and 12B via Unsloth
      model_load_infer.py     #   inference loading: base model 4-bit + peft adapter, no Unsloth
    inference.py              # run inference with a trained adapter (model_path picks it)
    trained_model.py          # where a job's weights are saved, how they are described, model_path validation
    bootstrap.py              # ping + train
  api/
    api.py                    # FastAPI app (training jobs, progress, infer, eval, datasets)
  providers/
    base.py                   # Provider (ABC), FunctionSpec, ImageSpec
    modal/                    # everything Modal specific; the only package that imports modal
      provider.py             # the Provider implementation
      deploy.py stop.py list_apps.py list_volumes.py inference_containers.py   # admin scripts, run by backends/modal_backend.py as `python -m providers.modal.<name>`
    __init__.py               # get_provider(name)
```

### One training path for both sizes

`google/gemma-4-E4B-it` (4B) uses the `gemma4` architecture and `google/gemma-4-12B-it` (12B) the newer `gemma4_unified` one. Both train through Unsloth (2026.10.x, which supports `transformers<=5.17` and `gemma4_unified`) in one image built from `requirements_train.txt` (CUDA 12.8 toolkit, torch 2.10+), and serve through plain `transformers` + `peft` + `bitsandbytes` in a separate, lighter image built from `requirements_infer.txt` (see [Images](#images)). The sizes differ only in their settings (`gemma4/model_config.py`), GPU and Modal function (`train_4b` / `train_12b`, `infer_4b` / `infer_12b`). Every job saves its LoRA adapter in a directory of its own, `/models/trained/<job_id>`, so no job (of either size) overwrites an earlier model; see [Trained models](#trained-models). See [training/model_sizes.md](model_sizes.md).

### Images

`app.py` declares four images (`ImageSpec`); every function names the one it runs in. Code is mounted last, so a code change never rebuilds the dependency layers.

| Image | Base | Requirements | Used by |
| --- | --- | --- | --- |
| `CPU_IMAGE` | Debian slim | none | `ping`, dataset checks, the `train` and `infer` routers |
| `WEB_IMAGE` | Debian slim | `requirements_web.txt` | the web API (`serve_api`) |
| `UNSLOTH_IMAGE` (training) | `nvidia/cuda:12.8.1-devel` | `requirements_train.txt` | `train_4b`, `train_12b` |
| `INFER_IMAGE` (inference only) | Debian slim (no CUDA toolkit) | `requirements_infer.txt` | `infer_4b`, `infer_12b` |

The inference image is kept as light as possible: it has only `torch`, `transformers`, `peft`, `accelerate`, `bitsandbytes` and `modal`. No Unsloth (and none of what it pulls in), no `trl`, `datasets`, `torchao` or `torchvision`, and no CUDA toolkit: the torch wheels bring the CUDA libraries they need and Modal provides the driver (`ImageSpec(gpu=True, cuda_toolkit=False)`). That makes the build and the cold start of an inference container smaller than with the training image. The adapter a training job saves is a standard peft LoRA adapter, so inference loads the base model 4-bit (nf4) with `bitsandbytes` and attaches the adapter with `peft` (`model_load_infer.py`). Keep the `torch` and `transformers` ranges of `requirements_train.txt` and `requirements_infer.txt` in step. Redeploy the Modal app to build the new image.

### Running it (Modal)

Needs the `modal` CLI, logged in (`pip install modal && modal setup`), and a provider secret holding `HF_TOKEN` for the Gemma weights (`SIDECAR_HF_SECRET`).

```sh
cd sidecar/ai_train
modal run app.py                                   # ping: image, volumes, GPU info, whether HF_TOKEN is set
modal run app.py --action datasets                 # list the local datasets (bin/datasets_training/<name>/dataset.toml)
modal run app.py --action sync --dataset <name>    # upload a local dataset to the data volume (only if it changed)
modal run app.py --action validate --dataset <name>  # check the dataset on the data volume
modal run app.py --action train --dataset <name> --train-steps 100 --model-size 4b   # 4b on A10G, 12b on A100
modal run app.py --action infer --model-size 4b --prompt "..."     # run the fine-tuned model
modal serve app.py                                 # web API in dev mode (`modal deploy app.py` to persist)
```

`train` syncs the (local) dataset first; `--dataset` is required for `sync`, `validate` and `train` (there is no default dataset). Web API endpoints: `POST /training/job_start`, `GET /training/job_progress?job_id=` (all events so far), `GET /training/job_events` (SSE), `POST /infer/query` (`model_path`: `trained/<job id>` picks the weights), `POST /evals/add_to_dataset`, `GET /evals/get_dataset_count`, `GET /healthz`. `--model-path trained/<job id>` picks the weights of `--action infer`.

`POST /training/job_start` (body `{dataset, num_train_steps?, model_size?, dataset_content?}`) answers with a `TrainingJob` (`job_id`, `provider_job_id`, `status`, `model_size`, `num_train_steps`, `dataset`, `started_at`). `job_id` is assigned by the sidecar and names the job's event log; `provider_job_id` is the compute provider's id of the spawned call. `dataset` is required: the file name of a `*.jsonl` on the data volume, `<dataset name>.jsonl` (a plain name, else 400; missing, 400; there is no default dataset). The sidecar has **no dataset endpoints**: datasets are owned by the Go server. When the Go server starts a job on one of its datasets it sends the file inline as `dataset_content` (base64); the sidecar stores it on the data volume as `dataset` (replacing an earlier file of that name) before it spawns the job. Without `dataset_content`, `dataset` must already be on the volume (else 400). `dataset_content` without `dataset`, or not valid base64, is a 400. Inside the sidecar a dataset is passed around as the pydantic `TrainingDataset` (`api/models.py`: `filename`, `content` (base64, never serialized), `size_bytes`, `uploaded_at`, `modified_at`; `payload()`, `from_path()`).

### Training progress, end to end

1. The training container appends **events** to the job's **event log** (`ai_train_core/progress_events.py`, `event_log.py`), which lives in a Modal Dict named `<SIDECAR_APP_NAME>-events` (keys `<job_id>:head`, `<job_id>:c<n>` for chunks of 50 events, `<job_id>:meta`, `<job_id>:stop`; entries expire 7 days after their last use, which is how old jobs go away; the Go database is the permanent record). There is no volume, commit or reload involved. Each event is one request to the Dict, and an event that cannot be stored is retried; a `progress` or `log` event that still fails is skipped, a `status` event that fails raises: `{"type":"status","status":"running"|"training"|"completed"|"failed"|"cancelled", ...}` (`completed` carries `final_loss` and `model`, the description of the saved weights, see [Trained models](#trained-models); `failed` and `cancelled` the `error`; non-finite numbers are sent as `null`), `{"type":"log","message"}` for what the job is doing (loading the model, ...) and `{"type":"progress","step","loss","learning_rate","epoch","elapsed_seconds"}` for each Trainer step (`logging_steps=1`). `POST /training/job_cancel {"job_id"}` makes the web container set the job's stop flag in the Dict; the training container looks at it between steps and ends the job with `cancelled` (the stop takes effect at the next step).
2. `GET /training/job_events?job_id=<id>[&from=<n>]` streams the log as **SSE**: `id: <event index>`, `event: status|progress|log`, `data: <event JSON>`, with `: keepalive` comments while idle. It ends after `completed`/`failed`/`cancelled`, or after a few minutes (the reader reconnects with `from=<last id + 1>`). Because the log keeps every event of the job, a Go server (or sidecar) that was offline while the job trained gets all the events it missed when it comes back; nothing depends on who was connected. **Watchdog:** a job whose container is killed (out of memory, timeout) cannot report its end, so while a stream is idle (every 15 s) the web container asks Modal about the job's call (`FunctionCall`, whose id `job_start` stores in the `meta` key); if the call is over and the log has no final event, it writes the `failed` event itself.
3. The Go server follows that stream for every job started through the training API (`internal/api/training/monitor.go`): it stores each event in `gb_training_job_events`, keeps the job's `gb_training_job_runs` row current (`status`, `last_step`, `last_loss`, `final_loss`, `error`, `finished_at`), and forwards the events to its SSE feed (`/v1/gb/training/events`). After a restart it resumes the jobs that are still unfinished from the last stored event. If the sidecar never reports an end within 3 hours of the job's start, the job is marked `failed`.
4. `GET /v1/gb/training/events[?db=<name>][&run_id=<id>]` is the SSE feed (the admin UI uses it) (`event: status|progress`, `data: {db, run_id, job_id, seq, type, ...}`). With `run_id` it first replays the job's stored events (resuming after `Last-Event-ID`), then goes live; without it, it is the live events of all jobs.

### Trained models

A trained model is a set of weights: for the LoRA fine-tuning the sidecar does, the few files of an adapter (`adapter_config.json`, `adapter_model.safetensors`, the tokenizer files). Each job that **completes** makes a **new** trained model, one row of the `gb_trained_models` table, so a user can train many models of the same type with other data, steps or size, and every one stays available to run inference on. A cancelled or failed job saves nothing.

#### Where the weights are saved

The weights live on the provider's **models volume** (Modal: `SIDECAR_VOL_MODELS`, default `gligabao-train-models`, mounted at `/models` in the containers). Each job gets a directory of its own, named after the job's ID, and nothing ever writes into another job's directory:

```
/models                              models volume
  trained/
    <job_id>/                        one directory per completed job (the sidecar's job ID: 16 hex characters)
      adapter_config.json            the LoRA settings; it names the base model the adapter was trained on
      adapter_model.safetensors      the LoRA weights themselves
      tokenizer files …              whatever the tokenizer's save_pretrained writes (tokenizer.json, chat template, …)
  finetuned_model_4b/                legacy: the single adapter per size that versions before 0.38.0 saved (overwritten
  finetuned_model_12b/               by every job of that size); not in the table, still loadable without model_path
```

- **Only the adapter is saved, not the base model.** The base model (`google/gemma-4-E4B-it` or `google/gemma-4-12B-it`) is downloaded from Hugging Face into the **HF cache volume** (`SIDECAR_VOL_HF_CACHE`, mounted at `/root/.cache/huggingface`) and shared by all jobs. `adapter_config.json` names it, and inference loads it (4-bit) and attaches the adapter. A trained model therefore needs the base model to stay downloadable (`HF_TOKEN`, see `SIDECAR_HF_SECRET`) and its `model_size` to be right. The adapter is small next to the base model (the code's estimate is 64 MB and up; the 12B one is larger); `size_bytes` has the real number.
- **Who writes it.** Only the training container, as the last stage of a job: it saves the adapter, commits the models volume (`commit_fn([MODELS_DIR])`), then lists the files and hashes them. Only after that does the job report `completed`, so a model row never points at weights that were not committed. A job that fails or is cancelled earlier saves nothing; one that dies *while* saving can leave a partial directory, which has no row.
- **Who reads it.** The inference containers mount the models and HF cache volumes (`INFER_VOLS` in `app.py`). A container that was already running does not see a volume change committed elsewhere, so `infer_response` reloads the models volume first; that is how an adapter trained a minute ago is found. The web container mounts neither volume (it only needs data and eval).
- **Never overwritten, never deleted.** A job ID is used once, so a path is never written twice. gligabao has no endpoint that deletes weights or a model row; to free space, remove the directory yourself (`modal volume rm -r gligabao-train-models trained/<job_id>`); the row stays and inference on it then answers `502` with the sidecar's "No trained model at …" message.
- **Looking at them.** The admin UI's **Volumes** button (training view, on the app's row) shows the volume's entry count; `modal volume ls gligabao-train-models trained/<job_id>` lists a model's files and `modal volume get gligabao-train-models trained/<job_id> ./out` downloads them. The row's `files` (path, size, sha256 of each) lets you verify a copy with `sha256sum`.
- **Runs outside the server.** `modal run app.py --action train` also saves to `trained/<job_id>` (the job ID is a Unix timestamp there), but it bypasses the sidecar and the monitor, so no row is made; such a model can still be used with `modal run app.py --action infer --model-path trained/<timestamp>`.

#### How a model is recorded

1. The training container saves the adapter in `/models/trained/<job_id>` and lists its files with their sizes and sha256 (`ai_train_core/trained_model.py`).
2. The job's `completed` event carries that as `model`: `{base_model, model_size, path, files: [{path, size_bytes, sha256}], size_bytes, num_examples, dataset_sha256, params}`. `path` is relative to the models volume; `dataset_sha256` is the digest of the dataset file the container actually read (the file of that name can be uploaded again with other content); `params` are the LoRA rank and alpha, dropout, learning rate, batch size, gradient accumulation, sequence length and 4-bit flag.
3. The Go monitor records it **before** it stores the event (`internal/api/training/monitor.go`), so a failure to record makes the event stream retry instead of losing the model. The row (`gb_trained_models`): `run_id` (the job's `gb_training_job_runs` row; null if that row is deleted later), unique `job_id`, `name` (made from size, dataset, steps and the start of the job ID, e.g. `4b-mine.jsonl-100steps-1a2b3c4d`; not unique, there is no rename), `base_model`, `model_size`, `dataset`, `dataset_sha256`, `num_examples`, `num_train_steps`, `final_loss`, `params`, `path`, `files`, `size_bytes`, `created_at`. `params` and `files` are JSON text on both SQLite and PostgreSQL. `job_id` is unique and the insert ignores a duplicate, so a replayed `completed` event (a restart) makes no second row.
4. A `completed` event without `model` (a Modal app that was not redeployed) records nothing and logs a warning. Existing databases get the table the next time the server starts (`CREATE TABLE IF NOT EXISTS`); there is nothing to migrate.

#### How inference runs

```
client / admin UI ── POST /v1/gb/training/models/{id}/infer ──► Go server (training.InferOf)
                                      reads the row: model_size, path (the client sends neither)
                                                   │  sidecar.Client.Infer
                                                   ▼
                          sidecar: POST /infer/query {prompt, model_size, model_path, …}   (routes.py, forwarded as is)
                                                   │  proxied by the modal backend
                                                   ▼
                          Modal web API (api/api.py): validates model_size and model_path
                                                   │  provider.call(infer, …)
                                                   ▼
                          infer (CPU router, app.py) ─► infer_4b / infer_12b (GPU function of that size)
                                                   │  inference.infer_response
                                                   ▼
                          reload the models volume ─► resolve_model_dir("trained/<job_id>") ─► transformers loads the
                          base model (4-bit) and peft attaches the adapter ─► generate ─► {"response", …}
```

- **`model_path`** is `trained/<job_id>` and nothing else: the web API refuses anything with another shape (`400`), and `resolve_model_dir` checks again inside the container, so a path cannot leave the `trained/` directory. A directory without `adapter_config.json` is a "No trained model at …" error. Without `model_path` the legacy `finetuned_model_<size>` adapter is used.
- **`model_size` must match the weights.** It chooses the GPU function (4b: A10G, 12b: A100). The Go endpoint passes the size recorded for the model; a direct caller of `/infer/query` has to pass the right one, or the wrong GPU may run out of memory.
- **Cold and warm requests.** A request that finds no GPU container holding the model is *cold*: Modal starts a container, which loads the base model from the HF cache (4-bit) and attaches the adapter, so it takes up to minutes. The container then keeps the model in memory and stays up for `SIDECAR_INFER_IDLE_TIMEOUT_S` (default 300 seconds) after its last request, so the requests that follow are *warm* and only generate; a request for another model replaces the loaded one (one model per container). Step by step, what a cold request does in the container, and how to read it: [Cold and warm starts](modal.md#cold-and-warm-starts). The Modal functions have a 600 second timeout per request (`app.py`; the model load counts); the Go client waits up to 15 minutes (`context` limit, no HTTP timeout).
- **The answer** is `{"response", "model_size", "model_path", "adapter", "cold_start", "load_seconds", "input_tokens", "output_tokens"}` (`cold_start`: this request loaded the model; `load_seconds`: how long that took, container start not included; 0 when warm). Generation samples (`do_sample`) with the given `temperature` (default 0.7) up to `max_tokens` (default 1024; the Go endpoint allows at most 8192). The prompt is wrapped in the same Gemma turn markers as the training data (`gemma4/prompt_format.py`).
- **Errors.** The Go endpoint answers `400` for a bad body, `404` for an unknown model, `502` if the sidecar fails (with its message when the generation itself failed, such as missing weights; a generic message otherwise) and `503` without a sidecar. The sidecar reports a failed generation as `200 {"error": "…"}`, which the Go client turns into `*sidecar.InferError`.

#### Adding an inference endpoint

There are three layers, and a new endpoint touches one or more of them. Redeploy the Modal app after a change to the Python side (the training view's `Redeploy`), or the running app keeps the old code.

1. **A new Go endpoint over trained models** (no Python change; for example a batch call, or a different default):
   - write the handler next to `InferOf` in `internal/api/training/models.go`: pick the database (`pick`), read the model with `trainingdb.GetTrainedModel`, and take `ModelSize` and `Path` **from the record, never from the request**;
   - if it needs something new from the sidecar, add a method to `internal/sidecar/inference.go` (a long call uses `postVia(ctx, c.stream, …)`, which has no overall timeout) and to the `Inferer` interface in `models.go` (the fake sidecar of the tests implements it);
   - register it in `registerTraining` (`internal/server/server.go`);
   - test it with the fake sidecar (`internal/api/training/models_test.go`).
2. **A new sidecar route** (a new kind of request to the Modal app):
   - add `("METHOD", "/path")` to `ROUTES` in `sidecar/routes.py`: the sidecar forwards the route to its backend untouched, it does not parse the body;
   - add the handler to `create_api` in `sidecar/ai_train/api/api.py`: validate the input (`bad_model_size`, `valid_model_path`), call an injected function with `run_in_threadpool`;
   - give `create_api` that function from `serve_api` in `app.py`, built with `provider.call(...)` (the API module never imports a provider);
   - add the Go client method (step 1) and a test in `ApiTest` (`test_ai_train_core.py`) with a fake in place of the function.
3. **A new generation function** (another GPU function, e.g. another decoding mode or model type):
   - put the logic in `ai_train_core/inference.py` (heavy imports inside the function; `ai_train_core` never imports a provider SDK) and resolve any path with `resolve_model_dir`;
   - register it in `app.py` with `@provider.function(FunctionSpec(image=INFER_IMAGE, gpu=CONFIG.gpu_for(size), timeout_s=600, volumes=INFER_VOLS, secrets=secrets))`: the volumes must include the models and HF cache volumes, and the function must reload the models volume before it reads;
   - route to it from a CPU function like `infer`, which picks the function by `model_size`.

A different kind of weights (another model size, or a trainer that saves something other than a LoRA adapter) also needs its own `describe` result and loader; the row's `path` and `files` already hold any set of files under `trained/<job_id>`.

#### Details and limits

- Listing and reading are plain database calls and need no sidecar; only inference does.
- The row is the record of what was trained, not proof the weights still exist: gligabao does not check the volume.
- No endpoint renames, deletes or exports a model, merges adapters, or streams the generated text; one request is one prompt and one answer.
- Training the same settings twice makes two models (and two directories); compare them by `final_loss`, `dataset_sha256` and `params`.

#### Endpoints

| Endpoint | |
|---|---|
| `GET /v1/gb/training/models[?db=][&limit=]` | `{"models": [...]}`, newest first (default 100, at most 500). |
| `GET /v1/gb/training/models/{id}[?db=]` | one trained model; 404 if unknown. |
| `POST /v1/gb/training/models/{id}/infer[?db=]` | body `{"prompt", "max_tokens"?, "temperature"?}`; runs the prompt through that model's weights on the sidecar (size and path come from the record) and answers `{"id", "model_id", "response", "status", "input_tokens", "output_tokens", "cold_start", "load_seconds", "duration_ms"}` (`cold_start`: the request loaded the model; `load_seconds`: how long that took; both are also stored in the request row). Every request is stored in `gb_trained_model_inferences` (prompt, response, status, error, ...), failed ones too; `id` is its row. It takes minutes when a GPU container has to start and load the model. 400 bad body, 404 unknown model, 502 sidecar failure (with its message if the generation failed, e.g. the weights are gone from the volume), 503 no sidecar. |
| `GET /v1/gb/training/models/{id}/inferences[?db=][&limit=]` | `{"inferences": [...]}`: the stored inference requests of that model (`id`, `trained_model_id`, `prompt`, `response`, `status` running/completed/failed, `error`, `max_tokens`, `temperature`, `input_tokens`, `output_tokens`, `cold_start`, `load_seconds`, `duration_ms`, `created_at`, `completed_at`; `cold_start` is null when unknown), newest first (default 100, at most 500); 404 unknown model. |

### Admin UI: the training view

The **training** view of the [admin UI](../../README.md) has a dataset upload (a file input and the table of uploaded datasets), a `Run` button (model size 4B/12B, dataset, steps), the list of the jobs in `gb_training_job_runs`, the list of the **trained models** in `gb_trained_models` (click one for its settings and files, and a form that runs a prompt through it), and for the selected job the event feed and a graph of its loss. The training endpoints are part of the API, under `/v1/gb/training/` (served whether or not the admin UI is on); the sidecar status stays with the admin UI:

| Endpoint | |
|---|---|
| `GET /v1/gb/admin/api/sidecar/status` | `{"sidecar_up": bool, "sidecar_active": bool}`. `sidecar_up` is a live health check of the sidecar (the admin UI's `sidecar:` badge in the top bar, refreshed every 15 s); `sidecar_active` is false if it failed its health check on startup (the training view then shows a "Sidecar not active" notice and disables `Run`; only a server restart changes it). |
| `POST /v1/gb/training/jobs[?db=]` | body `{"dataset", "num_train_steps"?, "model_size"?}`; `dataset` is the name of a training dataset (required: no name, or a name that is not in the training datasets directory, is a 400); starts a job, answers 201 with the stored row. 400 for a bad body or a dataset the sidecar rejects, 502 if the sidecar fails, 503 if the sidecar is not active (it failed its health check on startup). |
| `POST /v1/gb/training/jobs/{id}/stop[?db=]` | asks the sidecar to stop the job run `{id}` (the admin UI's **Stop job** button): 202 `{"id","status":"stopping"}`; 404 unknown run, 409 already over, 502 sidecar failure, 503 no sidecar. The job ends with a `cancelled` event at its next step. |
| `GET /v1/gb/training/jobs[?db=][&limit=]` | `{"jobs": [...]}`, newest first (default 50, at most 500). |
| `POST /v1/gb/training/datasets[?db=]` | uploads a JSONL training dataset: multipart form, the file in the field `file` (optionally `filename`). **The sidecar is not involved.** The server validates it (UTF-8, each non-blank line a JSON object with string `instruction` and `output`, optional string `input`; plain `*.jsonl` name; at most 64 MB) and saves it as a dataset of its own: the subdirectory named like the file (without `.jsonl`) of the training datasets directory, with a `dataset.toml` manifest it writes if there is none, and records it in `gb_training_datasets` (re-uploading a name replaces the file). Answers 201 with the dataset; 400 for an invalid file, 409 if a dataset of that name has a manifest of its own, 413 if too large. |
| `GET /v1/gb/training/datasets[?db=]` | `{"datasets": [...], "problems": [...]}`: the training datasets found in the training datasets directory by their `dataset.toml` manifests, by name (`name`, `description`, `format`, `file`, `dir`, `version`, `generator`, and the record: `filename`, `size_bytes`, `num_records`, `sha256`, `status` `stored`/`synced`, `updated_at`, `synced_at`), and the subdirectories whose manifest cannot be used (`dir`, `error`). The sidecar is not asked. |
| `GET /v1/gb/training/datasets/preview?name=[&lines=][&db=]` | the first lines (default 100, at most 1000) of a dataset's data file: `{"name", "lines", "num_records", "truncated"}`; 404 for an unknown dataset. |
| `GET /v1/gb/training/events[?db=][&run_id=]` | the SSE feed above. |

The training datasets are the subdirectories of the **training datasets directory** (`serve --training-dataset-dir`, default `bin/datasets_training` next to `bin/gligabao.db`) that have a `dataset.toml` manifest; the manifest names the dataset and says where its JSONL file is, and the dataset's own subdirectory also holds the script that generates the data. The server scans the directory for manifests every time it lists the datasets or starts a job (a dataset added by hand is there at once) and keeps a `gb_training_datasets` record per dataset (size, records, digest, `synced` or not). The layout, the manifest's keys and the rules are in [training/datasets.md](datasets.md). When a job is started on a dataset, the server sends its file to the sidecar inside the `POST /training/job_start` request (`dataset_content`) and marks the record `synced` once the sidecar has taken it; if the sidecar fails the job is not started (502). A job needs a dataset: there is no default one. The evals of the trained models are read the same way, from the **training evals directory** (`serve --training-eval-dir`, default `bin/evals_training`).

The sidecar's address is `sidecar_url` / `SIDECAR_URL` (default `http://127.0.0.1:8787`).

Local tests: `cd sidecar/ai_train && python -m unittest test_ai_train_core -v`.

### Configuration

All settings are in `config.py` and can be overridden by environment variable:

| Variable | Default | Meaning |
|---|---|---|
| `SIDECAR_PROVIDER` | `modal` | Compute provider (only `modal` exists). |
| `SIDECAR_APP_NAME` | `gligabao-train` | Name of the provider app. |
| `SIDECAR_GPU` | *(empty)* | GPU override for train/infer of every model size (Modal names, e.g. `a100`). Empty means the per-size default: `a10g` for 4b, `a100` for 12b. |
| `SIDECAR_TRAIN_TIMEOUT_S` | `7200` | Timeout of the train jobs. |
| `SIDECAR_INFER_IDLE_TIMEOUT_S` | `300` | How long a GPU inference container, with its model loaded, stays up after its last request (Modal: 2 to 1200). Requests within that time skip the model load. See [Warm inference containers](modal.md#warm-inference-containers). |
| `SIDECAR_HF_SECRET` | *(empty)* | Name of a provider secret with `HF_TOKEN`. Empty means no secret is attached. |
| `SIDECAR_VOL_DATA` | `gligabao-train-data` | Volume mounted at `/data` (datasets). |
| `SIDECAR_VOL_MODELS` | `gligabao-train-models` | Volume mounted at `/models` (fine-tuned adapters). |
| `SIDECAR_VOL_EVAL` | `gligabao-train-eval` | Volume mounted at `/eval` (eval examples from the web API). |
| `SIDECAR_VOL_HF_CACHE` | `gligabao-train-hf-cache` | Volume mounted at `/root/.cache/huggingface` (base-model cache, avoids re-downloads). |

Volumes are created if missing.

### How the provider abstraction works

`app.py` only says *what* to run. It builds a `FunctionSpec` (image, GPU, timeout, volumes, secrets) and registers functions with `@provider.function(spec)` (or `@provider.asgi_app(spec)` for the web API). It runs them with `provider.call(fn, ...)` or `provider.spawn(fn, ...)` and persists/refreshes volumes with `provider.commit_volumes(paths)` / `provider.reload_volumes(paths)`. Images are described by a provider-neutral `ImageSpec` (CUDA base or not, requirements file); the provider decides how to build them, and mounts the `ai_train/` directory into the container at `/pkg`. Code in `ai_train_core/` and `api/` gets what it needs from the provider as injected callables (e.g. `commit_fn`).

Rules that keep it swappable:

- Nothing outside `providers/` imports a provider SDK, and `ai_train_core/` / `api/` (the code that runs in the container) never do.
- `app = provider.native_app` is a module-level object because provider CLIs such as `modal run` look for it.
- Code that runs remotely imports `ai_train_core` inside the function body, since it only exists in the container's `/pkg` mount, and the heavy ML imports exist only in the GPU images (Unsloth only in the training one; torch, transformers and peft in both).

To add a provider (e.g. AWS): implement `Provider` in `providers/aws/provider.py` and add one branch to `get_provider` in `providers/__init__.py`. If the new provider cannot express something in `FunctionSpec`, extend `FunctionSpec` rather than leaking provider details into `app.py`.

### Growing it

1. Add a dependency: put it in the requirements file of the image that needs it (an image is rebuilt only when its file changes; code changes do not rebuild it).
2. Add a model size: an entry in the family's `model_config.py` (`ai_train_core/gemma4/`), a train/infer function pair in `app.py`, and a pipeline module if it needs a new backend.
3. Add a model family: a package next to `gemma4/` with the same modules (`model_config`, `prompt_format`, `model_train`, `model_load_infer`; see `gemma4/__init__.py`) and its name in `MODEL_FAMILIES` (`ai_train_core/model_families.py`). Family-neutral code (`bootstrap.py`, `inference.py`, `training_common.py`) takes a `model_family` and never imports a family directly; the wire API does not carry one yet (default family).
3. Add another remote function: declare it in `app.py` with a `FunctionSpec` and a matching action in `main`.
4. Add an inference endpoint: see [Adding an inference endpoint](#adding-an-inference-endpoint).

## The container

`Dockerfile.sidecar` (in the repo root; the plain `Dockerfile` is the Go server's) packages the sidecar on `python:3.13-slim`: it installs the requirements, copies `main_sidecar.py`, `sidecar_config.py`, `routes.py`, `backends/` and `ai_train/` (what the deploy action uploads to Modal), runs as the non-root user `sidecar` and starts `python main_sidecar.py`. The image sets `SIDECAR_HOST=0.0.0.0` (a container must listen on all interfaces for a published port to reach it) and exposes `8787`.

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
5. A route answered by the sidecar itself, like `/health`, goes in `sidecar/main_sidecar.py` instead (and any new package in `sidecar/requirements_sidecar.txt`). If the Dockerfile must copy new files, update `Dockerfile.sidecar`.
6. Document the endpoint in the tables above.
