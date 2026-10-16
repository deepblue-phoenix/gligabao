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
    GET  /backends/{name}/apps                      the active apps of this system: {"apps": [...]}; 404 if it has none
    .../backends/{name}/apps/{app}/actions/...      the same action routes, for one app of the backend (the plain
                                                    ones above are those of the backend's own, configured app)
    """
    def find(request: Request):
        p = request.path_params
        if p["name"] != backend.name:
            return None, JSONResponse({"error": f"no backend {p['name']!r}"}, status_code=404)
        actions = backend.app_actions(p["app"]) if "app" in p else backend.actions()
        runner = actions.get(p["action"])
        if runner is None:
            where = f"app {p['app']!r} of backend" if "app" in p else "backend"
            return None, JSONResponse({"error": f"no action {p['action']!r} on {where} {p['name']!r}"}, status_code=404)
        return runner, None

    async def start(request: Request) -> Response:
        runner, err = find(request)
        if err:
            return err
        try:
            return JSONResponse(await runner.start(), status_code=202)
        except ActionBusy as e:
            return JSONResponse({"error": str(e)}, status_code=409)

    async def cancel(request: Request) -> Response:
        runner, err = find(request)
        if err:
            return err
        try:
            return JSONResponse(await runner.cancel(), status_code=202)
        except ActionNotRunning as e:
            return JSONResponse({"error": str(e)}, status_code=409)

    async def get(request: Request, from_: int = Query(0, alias="from", ge=0)) -> Response:
        runner, err = find(request)
        return err or JSONResponse(runner.snapshot(from_))

    async def events(request: Request, from_: int = Query(0, alias="from", ge=0)) -> Response:
        runner, err = find(request)
        if err:
            return err
        return StreamingResponse(runner.follow(from_), media_type="text/event-stream",
                                 headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})

    for base in ("/backends/{name}", "/backends/{name}/apps/{app}"):
        app.add_api_route(base + "/actions/{action}", start, methods=["POST"], include_in_schema=False)
        app.add_api_route(base + "/actions/{action}/cancel", cancel, methods=["POST"], include_in_schema=False)
        app.add_api_route(base + "/actions/{action}", get, methods=["GET"], include_in_schema=False)
        app.add_api_route(base + "/actions/{action}/events", events, methods=["GET"], include_in_schema=False)

    def lister(what: str, call):
        async def handler(name: str) -> Response:
            if name != backend.name:
                return JSONResponse({"error": f"no backend {name!r}"}, status_code=404)
            try:
                items = await call()
            except BackendError as e:
                return JSONResponse({"error": str(e)}, status_code=502)
            if items is None:
                return JSONResponse({"error": f"backend {name!r} has no {what}"}, status_code=404)
            return JSONResponse({what: items})
        return handler

    app.add_api_route("/backends/{name}/volumes", lister("volumes", backend.volumes), methods=["GET"], include_in_schema=False)
    app.add_api_route("/backends/{name}/apps", lister("apps", backend.apps), methods=["GET"], include_in_schema=False)
