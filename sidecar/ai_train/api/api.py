"""HTTP API in front of the training/inference functions. Provider neutral: no modal/aws imports.

Runs inside the web container. Everything provider-specific is injected as callables by
app.py (spawn a job, call a function, commit/reload volumes).

Endpoints:
    POST /training/job_start   {num_train_steps?, model_size?, dataset?, dataset_content?}  -> spawns a job, returns a TrainingJob (api/models.py)
    GET  /training/job_progress[?job_id=]                        -> the job's (default: newest) progress entries
    GET  /training/job_events?job_id=[&from=N]                   -> SSE stream of the job's progress events (see below)
    POST /infer/query                  {prompt, max_tokens?, temperature?, model_size?}
    POST /evals/add_to_dataset    {prompt, response, notes?}
    GET  /evals/get_dataset_count
    GET  /healthz

There are no dataset endpoints: training datasets are managed by the Go server (its database and files). The sidecar only
stores a dataset internally: a job_start that carries `dataset_content` (base64 of the jsonl file) saves it on the data
volume as `dataset` before spawning the job; without `dataset_content`, `dataset` must already be on the volume.

GET /training/job_events is Server-Sent Events: the events of the job's progress file (ai_train_core/progress_events.py)
as they are written, each as `id: <index>`, `event: <status|progress>`, `data: <the event as JSON>`. The stream ends
after the "completed" or "failed" event, or after `max_stream_s` (the reader reconnects with `from=<last id + 1>`).
Comment lines (": keepalive") are sent while there is nothing new.
"""
import asyncio
import json
import time
import uuid
from datetime import datetime
from pathlib import Path
from typing import Callable

from fastapi import FastAPI, Query, Request
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import JSONResponse, StreamingResponse

from api.models import TrainingDataset, TrainingJob
from ai_train_core.dataset import store_dataset, valid_dataset_filename
from ai_train_core.model_config import MODEL_SIZES
from ai_train_core.paths import DATA_DIR, EVAL_DATASET_PATH, EVAL_DIR, PROGRESS_DIR
from ai_train_core.progress_events import is_terminal, progress_path, read_events, valid_job_id


def create_api(
    spawn_training_job: Callable[..., str],     # (num_train_steps, model_size, dataset, job_id) -> the provider's call id
    generate_response: Callable[..., dict],     # (prompt, max_tokens, temperature, model_size) -> dict
    commit_volumes: Callable[[list], None],
    reload_volumes: Callable[[list], None],
    poll_interval_s: float = 2.0,               # how often /training/job_events looks for new events
    max_stream_s: float = 300.0,                # how long one /training/job_events response lasts at most
) -> FastAPI:
    api = FastAPI(docs_url=None, redoc_url=None, openapi_url=None)

    def bad_model_size(model_size):
        if str(model_size).lower() not in MODEL_SIZES:
            return JSONResponse({"error": f"Invalid model_size '{model_size}'. Must be one of {list(MODEL_SIZES)}."},
                                status_code=400)

    @api.post("/training/job_start")
    async def job_start(request: Request):
        data = await request.json()
        num_train_steps = data.get("num_train_steps", 100)
        model_size = str(data.get("model_size", "4b")).lower()
        dataset = data.get("dataset") or ""
        dataset_content = data.get("dataset_content") or ""
        if (err := bad_model_size(model_size)):
            return err
        if isinstance(num_train_steps, bool) or not isinstance(num_train_steps, int) or num_train_steps < 1:
            return JSONResponse({"error": "num_train_steps must be a positive integer"}, status_code=400)
        if dataset:
            if not valid_dataset_filename(dataset):
                return JSONResponse({"error": f"Invalid dataset '{dataset}': must be a plain *.jsonl file name"},
                                    status_code=400)
            if dataset_content:
                try:
                    payload = TrainingDataset(filename=dataset, content=dataset_content).payload()
                except ValueError as e:
                    return JSONResponse({"error": str(e)}, status_code=400)
                await run_in_threadpool(store_dataset, dataset, payload, commit_volumes, DATA_DIR)
            else:
                await run_in_threadpool(reload_volumes, [DATA_DIR])
                if not (Path(DATA_DIR) / dataset).is_file():
                    return JSONResponse({"error": f"Dataset '{dataset}' not found"}, status_code=400)
        elif dataset_content:
            return JSONResponse({"error": "dataset_content needs a dataset name"}, status_code=400)
        job_id = uuid.uuid4().hex[:16]  # names the progress file; the provider's own id is only for its dashboard
        provider_job_id = await run_in_threadpool(spawn_training_job, num_train_steps, model_size, dataset, job_id)
        job = TrainingJob(job_id=job_id, provider_job_id=str(provider_job_id), model_size=model_size,
                          num_train_steps=num_train_steps, dataset=dataset)
        return JSONResponse(job.model_dump(mode="json"))

    @api.get("/training/job_progress")
    async def job_progress(job_id: str = ""):
        if job_id and not valid_job_id(job_id):
            return JSONResponse({"error": "invalid job_id"}, status_code=400)
        # This container is long-lived, so its view of the volume can be stale: the training
        # container commits progress as it logs; reload to see it.
        await run_in_threadpool(reload_volumes, [PROGRESS_DIR])
        if job_id:
            latest = Path(progress_path(job_id))
        else:
            files = list(Path(PROGRESS_DIR).glob("training_progress_*.jsonl"))
            if not files:
                return JSONResponse({"progress": []})
            latest = max(files, key=lambda f: f.stat().st_mtime)
        return JSONResponse({"progress": read_events(str(latest))})

    @api.get("/training/job_events")
    async def job_events(request: Request, job_id: str = "", start: int = Query(0, alias="from", ge=0)):
        if not valid_job_id(job_id):
            return JSONResponse({"error": "job_id is required (letters, digits, - and _)"}, status_code=400)
        path = progress_path(job_id)

        async def stream():
            pos = start
            deadline = time.monotonic() + max_stream_s
            yield ": connected\n\n"
            while time.monotonic() < deadline:
                if await request.is_disconnected():
                    return
                # The training container commits progress as it logs; see job_progress.
                await run_in_threadpool(reload_volumes, [PROGRESS_DIR])
                events = await run_in_threadpool(read_events, path)
                if pos >= len(events):
                    yield ": keepalive\n\n"
                for ev in events[pos:]:
                    yield f"id: {pos}\nevent: {ev['type']}\ndata: {json.dumps(ev)}\n\n"
                    pos += 1
                    if is_terminal(ev):
                        return
                await asyncio.sleep(poll_interval_s)

        return StreamingResponse(stream(), media_type="text/event-stream",
                                 headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})

    @api.post("/infer/query")
    async def generate_response_endpoint(request: Request):
        data = await request.json()
        model_size = data.get("model_size", "4b")
        if (err := bad_model_size(model_size)):
            return err
        result = await run_in_threadpool(
            generate_response, data.get("prompt", ""), data.get("max_tokens", 1024),
            data.get("temperature", 0.7), model_size)
        return JSONResponse(result)

    @api.post("/evals/add_to_dataset")
    async def add_to_dataset(request: Request):
        data = await request.json()
        prompt, response = data.get("prompt", ""), data.get("response", "")
        if not prompt or not response:
            return JSONResponse({"error": "prompt and response are required"}, status_code=400)

        path = Path(EVAL_DATASET_PATH)
        path.parent.mkdir(parents=True, exist_ok=True)
        with open(path, "a") as f:
            f.write(json.dumps({"prompt": prompt, "response": response, "notes": data.get("notes", ""),
                                "added_at": datetime.now().isoformat()}) + "\n")
        await run_in_threadpool(commit_volumes, [EVAL_DIR])
        return JSONResponse({"status": "success", "message": "Example added to eval dataset"})

    @api.get("/evals/get_dataset_count")
    async def get_dataset_count():
        path = Path(EVAL_DATASET_PATH)
        count = 0
        if path.exists():
            with open(path) as f:
                count = sum(1 for _ in f)
        return JSONResponse({"count": count})

    @api.get("/healthz")
    async def healthz():
        return JSONResponse({"status": "ok", "timestamp": datetime.now().isoformat()})

    return api
