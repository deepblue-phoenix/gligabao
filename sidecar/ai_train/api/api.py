"""HTTP API in front of the training/inference functions. Provider neutral: no modal/aws imports.

Runs inside the web container. Everything provider-specific is injected as callables by
app.py (spawn a job, call a function, commit/reload volumes).

Endpoints:
    POST /training/job_start   {num_train_steps?, model_size?, dataset?, dataset_content?}  -> spawns a job, returns a TrainingJob (api/models.py)
    GET  /training/job_progress[?job_id=]                        -> the job's (default: newest) progress entries
    GET  /training/job_events?job_id=[&from=N]                   -> SSE stream of the job's progress events (see below)
    POST /training/job_cancel  {job_id}                          -> asks the job to stop (200 {job_id, status: "stopping"}); 404 unknown job, 409 already over
    POST /infer/query                  {prompt, max_tokens?, temperature?, model_size?, model_path?}
                                       model_path ("trained/<job id>", the path of a trained model that a completed job reported)
                                       picks the weights; model_size must be the size they were trained for
    POST /evals/add_to_dataset    {prompt, response, notes?}
    GET  /evals/get_dataset_count
    GET  /healthz

There are no dataset endpoints: training datasets are managed by the Go server (its database and files). The sidecar only
stores a dataset internally: a job_start that carries `dataset_content` (base64 of the jsonl file) saves it on the data
volume as `dataset` before spawning the job; without `dataset_content`, `dataset` must already be on the volume.

GET /training/job_events is Server-Sent Events: the events of the job's progress file (ai_train_core/progress_events.py)
as they are written, each as `id: <index>`, `event: <status|progress>`, `data: <the event as JSON>`. The stream ends
after the "completed", "failed" or "cancelled" event, or after `max_stream_s` (the reader reconnects with `from=<last id + 1>`).
Comment lines (": keepalive") are sent while there is nothing new.
"""
import asyncio
import json
import logging
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
from ai_train_core.progress_events import is_terminal, progress_path, read_events, request_stop, valid_job_id
from ai_train_core.trained_model import valid_model_path

log = logging.getLogger("uvicorn.error")  # shows in the container's log next to the access lines


def _describe(ev: dict) -> str:
    """A one-line summary of a progress event, for the log."""
    if ev["type"] == "progress":
        return f"step {ev.get('step')} loss {ev.get('loss')}"
    if ev["type"] == "log":
        return str(ev.get("message"))
    return f"status {ev.get('status')}"


def create_api(
    spawn_training_job: Callable[..., str],     # (num_train_steps, model_size, dataset, job_id) -> the provider's call id
    infer_response: Callable[..., dict],     # (prompt, max_tokens, temperature, model_size, model_path) -> dict
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

    @api.post("/training/job_cancel")
    async def job_cancel(request: Request):
        """Ask a job to stop. The training container sees the stop file between steps (and between the stages of its
        setup) and ends the job with a "cancelled" event, so the stop takes effect at the next step, not instantly."""
        try:
            data = await request.json()
        except ValueError:
            data = None
        job_id = data.get("job_id", "") if isinstance(data, dict) else ""
        if not valid_job_id(job_id):
            return JSONResponse({"error": "job_id is required (letters, digits, - and _)"}, status_code=400)
        await run_in_threadpool(reload_volumes, [PROGRESS_DIR])
        events = await run_in_threadpool(read_events, progress_path(job_id))
        if not events:
            return JSONResponse({"error": f"no such job {job_id!r}"}, status_code=404)
        if any(is_terminal(e) for e in events):
            return JSONResponse({"error": f"job {job_id!r} is already over"}, status_code=409)
        await run_in_threadpool(request_stop, job_id, lambda paths: commit_volumes(paths))
        log.info("training job %s: stop requested", job_id)
        return JSONResponse({"job_id": job_id, "status": "stopping"})

    @api.get("/training/job_events")
    async def job_events(request: Request, job_id: str = "", start: int = Query(0, alias="from", ge=0)):
        if not valid_job_id(job_id):
            return JSONResponse({"error": "job_id is required (letters, digits, - and _)"}, status_code=400)
        path = progress_path(job_id)

        async def stream():
            pos = start
            deadline = time.monotonic() + max_stream_s
            log.info("training job %s: event stream opened from %d", job_id, start)
            yield ": connected\n\n"
            polls = 0
            while time.monotonic() < deadline:
                if await request.is_disconnected():
                    log.info("training job %s: event stream closed by the reader at %d", job_id, pos)
                    return
                # The training container commits progress as it logs; see job_progress.
                try:
                    await run_in_threadpool(reload_volumes, [PROGRESS_DIR])
                except Exception as e:  # e.g. the volume is busy: read what is there and try again next time
                    log.warning("training job %s: reloading the progress volume failed: %r", job_id, e)
                events = await run_in_threadpool(read_events, path)
                polls += 1
                if pos >= len(events):
                    yield ": keepalive\n\n"
                    if polls % 15 == 0:  # about every 30 s
                        log.info("training job %s: nothing new after event %d (%d in the file)", job_id, pos - 1, len(events))
                for ev in events[pos:]:
                    yield f"id: {pos}\nevent: {ev['type']}\ndata: {json.dumps(ev)}\n\n"
                    log.info("training job %s: event %d sent: %s", job_id, pos, _describe(ev))
                    pos += 1
                    if is_terminal(ev):
                        return
                await asyncio.sleep(poll_interval_s)
            log.info("training job %s: event stream ends at its time limit (the reader reconnects from %d)", job_id, pos)

        return StreamingResponse(stream(), media_type="text/event-stream",
                                 headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})

    @api.post("/infer/query")
    async def infer_endpoint(request: Request):
        data = await request.json()
        model_size = data.get("model_size", "4b")
        if (err := bad_model_size(model_size)):
            return err
        model_path = data.get("model_path") or ""
        if model_path and not valid_model_path(model_path):
            return JSONResponse({"error": f"Invalid model_path '{model_path}': expected 'trained/<job id>'"}, status_code=400)
        result = await run_in_threadpool(
            infer_response, data.get("prompt", ""), data.get("max_tokens", 1024),
            data.get("temperature", 0.7), model_size, model_path)
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
