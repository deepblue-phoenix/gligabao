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

`side_car/ai_train/` is the starting point for running fine-tuning jobs on a cloud provider. It is modelled on the startup/bootstrap part of `gloflow_phoenix/gf_phoenix/ai_train` (app, volumes, images, secrets, CLI entrypoint) and deliberately leaves out the training details, the web API and inference. **Today it only verifies the setup**: a `ping` that reports what the remote container looks like, and a stub `train` that writes a progress file. It is separate from the FastAPI service above, is not imported by `main_sidecar.py`, and is not copied into `Dockerfile.sidecar`.

```
side_car/ai_train/
  app.py                      # declares the remote functions + CLI entrypoint (provider-neutral)
  config.py                   # TrainConfig, overridable by env vars
  requirements_train.txt      # deps of the remote train image (empty for now)
  ai_train_core/bootstrap.py  # runs inside the container: run_ping(), run_train() stub
  providers/
    base.py                   # Provider (ABC), FunctionSpec, ImageSpec
    modal_provider.py         # Modal implementation; the only file that imports modal
    __init__.py               # get_provider(name)
```

### Running it (Modal)

Needs the `modal` CLI, logged in (`pip install modal && modal setup`).

```sh
cd side_car/ai_train
modal run app.py                                   # ping: image, volumes, GPU info, whether HF_TOKEN is set
modal run app.py --action train --train-steps 5    # stub: writes /progress/training_progress_<ts>.jsonl, commits volumes
```

### Configuration

All settings are in `config.py` and can be overridden by environment variable:

| Variable | Default | Meaning |
|---|---|---|
| `SIDECAR_PROVIDER` | `modal` | Compute provider (only `modal` exists). |
| `SIDECAR_APP_NAME` | `gligabao-train` | Name of the provider app. |
| `SIDECAR_GPU` | *(empty)* | GPU for the `train` job, e.g. `a10g`, `a100` (Modal names, passed through). Empty means no GPU and a CPU-only base image, so testing costs almost nothing. |
| `SIDECAR_TRAIN_TIMEOUT_S` | `7200` | Timeout of the `train` job. |
| `SIDECAR_HF_SECRET` | *(empty)* | Name of a provider secret with `HF_TOKEN`. Empty means no secret is attached. |
| `SIDECAR_VOL_DATA` | `gligabao-train-data` | Volume mounted at `/data` (datasets). |
| `SIDECAR_VOL_MODELS` | `gligabao-train-models` | Volume mounted at `/models` (fine-tuned outputs). |
| `SIDECAR_VOL_PROGRESS` | `gligabao-train-progress` | Volume mounted at `/progress` (training progress). |

Volumes are created if missing.

### How the provider abstraction works

`app.py` only says *what* to run. It builds a `FunctionSpec` (image, GPU, timeout, volumes, secrets) and registers functions with `@provider.function(spec)`. It runs them with `provider.call(fn, ...)` or `provider.spawn(fn, ...)` and persists volume writes with `provider.commit_volumes()`. Images are described by a provider-neutral `ImageSpec` (CUDA base or not, requirements file); the provider decides how to build them, and mounts the `ai_train/` directory into the container at `/pkg`.

Rules that keep it swappable:

- Nothing outside `providers/` imports a provider SDK, and `ai_train_core/` (the code that runs in the container) never does.
- `app = provider.native_app` is a module-level object because provider CLIs such as `modal run` look for it.
- Code that runs remotely imports `ai_train_core` inside the function body, since it only exists in the container's `/pkg` mount.

To add a provider (e.g. AWS): implement `Provider` in `providers/aws_provider.py` and add one branch to `get_provider` in `providers/__init__.py`. If the new provider cannot express something in `FunctionSpec`, extend `FunctionSpec` rather than leaking provider details into `app.py`.

### Growing it

1. Add a dependency: put it in `requirements_train.txt` (the image is rebuilt only when that file changes; code changes do not rebuild it).
2. Add real training code under `ai_train_core/` and call it from `train` in `app.py`.
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
