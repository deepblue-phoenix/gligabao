"""The sidecar's REST API. This table is the contract with the Go server (internal/sidecar/); each route is answered by
the configured backend (see backends/), so the Go side never learns what runs behind it."""
from fastapi import FastAPI, Query, Request
from fastapi.responses import JSONResponse, Response, StreamingResponse
from starlette.background import BackgroundTask

from backends import Backend, BackendError
from backends.actions import ActionBusy, ActionNotRunning

# There are deliberately no dataset routes: training datasets belong to the Go server (database and files), which hands a
# dataset to the sidecar inside POST /training/job_start. The backend stores it internally.
ROUTES = (
    ("POST", "/training/job_start"),
    ("GET", "/training/job_progress"),
    ("GET", "/training/job_events"),       # SSE
    ("POST", "/infer/query"),
    ("POST", "/evals/add_to_dataset"),
    ("GET", "/evals/get_dataset_count"),
)


def add_backend_routes(app: FastAPI, backend: Backend) -> None:
    def make_handler(method: str, path: str):
        async def handler(request: Request) -> Response:
            try:
                resp = await backend.forward(method, path, request.url.query, await request.body(),
                                             request.headers.get("content-type"))
            except BackendError as e:
                return JSONResponse({"error": str(e)}, status_code=502)
            return StreamingResponse(resp.body, status_code=resp.status, media_type=resp.content_type,
                                     background=BackgroundTask(resp.close))
        return handler

    for method, path in ROUTES:
        app.add_api_route(path, make_handler(method, path), methods=[method], include_in_schema=False)


def add_action_routes(app: FastAPI, backend: Backend) -> None:
    """Routes answered by the sidecar itself (not forwarded): the backend's long-running actions (backends/actions.py).

    POST /backends/{name}/actions/{action}          start it: 202 + state, 409 if it is running
    POST /backends/{name}/actions/{action}/cancel   stop a running one (SIGINT, kill after 10 s): 202, 409 if not running
    GET  /backends/{name}/actions/{action}          state and the kept log (`?from=<i>`: lines from index i)
    GET  /backends/{name}/actions/{action}/events   SSE: `log` and `state` events, ends when the run is over
    GET  /backends/{name}/volumes                   the backend's storage volumes: {"volumes": [...]}; 404 if it has none
    """
    def find(name: str, action: str):
        runner = backend.actions().get(action) if name == backend.name else None
        if runner is None:
            return None, JSONResponse({"error": f"no action {action!r} on backend {name!r}"}, status_code=404)
        return runner, None

    @app.get("/backends/{name}/volumes", include_in_schema=False)
    async def volumes(name: str) -> Response:
        if name != backend.name:
            return JSONResponse({"error": f"no backend {name!r}"}, status_code=404)
        try:
            vols = await backend.volumes()
        except BackendError as e:
            return JSONResponse({"error": str(e)}, status_code=502)
        if vols is None:
            return JSONResponse({"error": f"backend {name!r} has no volumes"}, status_code=404)
        return JSONResponse({"volumes": vols})

    @app.post("/backends/{name}/actions/{action}", include_in_schema=False)
    async def start(name: str, action: str) -> Response:
        runner, err = find(name, action)
        if err:
            return err
        try:
            return JSONResponse(await runner.start(), status_code=202)
        except ActionBusy as e:
            return JSONResponse({"error": str(e)}, status_code=409)

    @app.post("/backends/{name}/actions/{action}/cancel", include_in_schema=False)
    async def cancel(name: str, action: str) -> Response:
        runner, err = find(name, action)
        if err:
            return err
        try:
            return JSONResponse(await runner.cancel(), status_code=202)
        except ActionNotRunning as e:
            return JSONResponse({"error": str(e)}, status_code=409)

    @app.get("/backends/{name}/actions/{action}", include_in_schema=False)
    async def get(name: str, action: str, from_: int = Query(0, alias="from", ge=0)) -> Response:
        runner, err = find(name, action)
        return err or JSONResponse(runner.snapshot(from_))

    @app.get("/backends/{name}/actions/{action}/events", include_in_schema=False)
    async def events(name: str, action: str, from_: int = Query(0, alias="from", ge=0)) -> Response:
        runner, err = find(name, action)
        if err:
            return err
        return StreamingResponse(runner.follow(from_), media_type="text/event-stream",
                                 headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})
