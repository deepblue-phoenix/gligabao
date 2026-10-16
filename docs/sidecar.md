# Python sidecar

`side_car/`, `Dockerfile.sidecar`, `run_build_sidecar.sh`, `internal/sidecar/sidecar.go`

The sidecar is a small Python service that runs next to the Go server and that the server talks to over HTTP. It exists for work that is easier to do in Python (libraries that have no good Go equivalent). **Today it is only a skeleton**: it starts, listens, and answers a health check. Nothing in the server calls it yet.

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

## The container

`Dockerfile.sidecar` (in the repo root; the plain `Dockerfile` is the Go server's) packages the sidecar on `python:3.13-slim`: it installs the requirements, copies `main_sidecar.py`, runs as the non-root user `sidecar` and starts `python main_sidecar.py`. The image sets `SIDECAR_HOST=0.0.0.0` (a container must listen on all interfaces for a published port to reach it) and exposes `8787`.

```sh
./run_build_sidecar.sh                         # builds gligabao-sidecar:latest
./run_build_sidecar.sh myrepo/sidecar:dev      # or a name of your choice
docker run --rm -p 127.0.0.1:8787:8787 gligabao-sidecar:latest
```

Publish the port on `127.0.0.1` (as above) unless the sidecar must be reachable from other machines: it has no authentication.

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
