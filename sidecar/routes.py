"""The sidecar's REST API. This table is the contract with the Go server (internal/sidecar/); each route is answered by
the configured backend (see backends/), so the Go side never learns what runs behind it."""
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, Response, StreamingResponse
from starlette.background import BackgroundTask

from backends import Backend, BackendError

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
