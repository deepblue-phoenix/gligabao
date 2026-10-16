# Python sidecar

`side_car/`, `side_car/ai_train/`, `Dockerfile.sidecar`, `run_build_sidecar.sh`, `internal/sidecar/sidecar.go`

The sidecar is a small Python service that runs next to the Go server and that the server talks to over HTTP. It exists for work that is easier to do in Python (libraries that have no good Go equivalent). **Today it is only a skeleton**: it starts, listens, and answers a health check. Nothing in the server calls it yet. Alongside it sits a separate, equally small bootstrap for remote (cloud GPU) training jobs; see [Remote training bootstrap](#remote-training-bootstrap).

## The service

`side_car/main_sidecar.py` is a [FastAPI](https://fastapi.tiangolo.com/) app run by [uvicorn](https://www.uvicorn.org/). Dependencies are in `side_car/requirements.txt`.

| Endpoint | Response |
|---|---|
| `GET /health` | `200 {"status": "ok"}` |

Configuration is by environment variable:

| Variable | Default | Meaning |
|---|---|---|
| `SIDECAR_HOST` | `127.0.0.1` | Interface to listen on. Local only by default, as the sidecar has no authentication. |
| `SIDECAR_PORT` | `8787` | Port to listen on. |

Run it without a container:

```sh
cd side_car
python3 -m venv .venv && . .venv/bin/activate
pip install -r requirements.txt
python main_sidecar.py              # listens on 127.0.0.1:8787
curl http://127.0.0.1:8787/health
```

## Remote training bootstrap

`side_car/ai_train/` fine-tunes Gemma-4 (4B and 12B, LoRA) on a cloud provider and serves the result. It is ported from `gloflow_phoenix/gf_phoenix/ai_train` and restructured so the cloud-specific parts sit behind a provider interface. It is separate from the FastAPI service above, is not imported by `main_sidecar.py`, and is not copied into `Dockerfile.sidecar`.

```
side_car/ai_train/
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
    progress.py               # Trainer callback writing live progress jsonl
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
cd side_car/ai_train
modal run app.py                                   # ping: image, volumes, GPU info, whether HF_TOKEN is set
modal run app.py --action sync                     # upload finetune_data/*.jsonl to the data volume (only if newer)
modal run app.py --action validate                 # check the remote dataset
modal run app.py --action train --train-steps 100 --model-size 4b     # 4b on A10G, 12b on A100
modal run app.py --action generate --model-size 4b --prompt "..."     # run the fine-tuned model
modal serve app.py                                 # web API in dev mode (`modal deploy app.py` to persist)
```

`train` syncs the dataset first. Web API endpoints: `POST /training/job_start`, `GET /training/job_progress`, `POST /infer/query`, `POST /evals/add_to_dataset`, `GET /evals/get_dataset_count`, `POST /datasets/upload`, `GET /datasets/list`, `GET /healthz`.

Local tests: `cd side_car/ai_train && python -m unittest test_ai_train_core -v`.

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

`Dockerfile.sidecar` (in the repo root; the plain `Dockerfile` is the Go server's) packages the sidecar on `python:3.13-slim`: it installs the requirements, copies `main_sidecar.py`, runs as the non-root user `sidecar` and starts `python main_sidecar.py`. The image sets `SIDECAR_HOST=0.0.0.0` (a container must listen on all interfaces for a published port to reach it) and exposes `8787`.

```sh
./run_build_sidecar.sh                         # builds gligabao-sidecar:latest
./run_build_sidecar.sh myrepo/sidecar:dev      # or a name of your choice
docker run --rm -p 127.0.0.1:8787:8787 gligabao-sidecar:latest
```

Publish the port on `127.0.0.1` (as above) unless the sidecar must be reachable from other machines: it has no authentication.

### With docker compose

`docker-compose.yml` starts the server and the sidecar on the same network (`gligabao`):

```sh
export ANTHROPIC_API_KEY=...
docker compose up --build
```

On that network the server reaches the sidecar at `http://sidecar:8787` (the service name; `127.0.0.1` would be the server's own container). The server starts only once the sidecar's `/health` check passes. Both ports are published on `127.0.0.1` only; the server's data lives in the `gligabao-data` volume.

## The Go client

`internal/sidecar/sidecar.go` is the only place in the Go code that knows the sidecar's HTTP API.

```go
c := sidecar.New("")                       // "" is sidecar.DefaultBaseURL, http://127.0.0.1:8787
resp, err := c.Health(ctx)                 // resp.Status == "ok"
```

- `sidecar.New(baseURL)` returns a `*Client` with a 30 second timeout per request. A trailing `/` on the URL is ignored.
- `Client.Health(ctx)` calls `GET /health`. It returns an error if the sidecar cannot be reached, answers with a status other than 200, sends a body that is not the expected JSON, or reports a `status` other than `"ok"`.
- Errors name the method and path (`sidecar GET /health: ...`) and, for a bad status, include the start of the response body.

The tests (`internal/sidecar/sidecar_test.go`) run the client against an `httptest` server, so they need neither Python nor Docker.

## Adding an endpoint

1. Add the route to `side_car/main_sidecar.py` (and any new package to `side_car/requirements.txt`).
2. Add a method to `Client` in `internal/sidecar/sidecar.go`, with typed request and response structs, next to `Health`. Keep all calls to the sidecar in that package.
3. If the Dockerfile must copy new files, update `Dockerfile.sidecar` (it copies `main_sidecar.py` only).
4. Document the endpoint in the table above.
