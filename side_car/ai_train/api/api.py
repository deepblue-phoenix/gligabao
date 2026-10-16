"""HTTP API in front of the training/inference functions. Provider neutral: no modal/aws imports.

Runs inside the web container. Everything provider-specific is injected as callables by
app.py (spawn a job, call a function, commit/reload volumes).

Endpoints:
    POST /training/job_start   {num_train_steps?, model_size?, dataset?}  -> spawns a job, returns a TrainingJob (api/models.py)
    GET  /training/job_progress[?job_id=]                        -> the job's (default: newest) progress entries
    GET  /training/job_events?job_id=[&from=N]                   -> SSE stream of the job's progress events (see below)
    POST /infer/query                  {prompt, max_tokens?, temperature?, model_size?}
    POST /evals/add_to_dataset    {prompt, response, notes?}
    GET  /evals/get_dataset_count
    POST /datasets/upload              {filename, content (base64 or plain jsonl)}  -> stored as DATA_DIR/<filename>
    GET  /datasets/list
    GET  /healthz

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
from pydantic import ValidationError

from api.models import TrainingDataset, TrainingJob
from ai_train_core.dataset import valid_dataset_filename
from ai_train_core.model_config import MODEL_SIZES
from ai_train_core.paths import (DATA_DIR, EVAL_DATASET_PATH, EVAL_DIR, PROGRESS_DIR,
                                 REMOTE_METADATA_PATH)
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
        if (err := bad_model_size(model_size)):
            return err
        if isinstance(num_train_steps, bool) or not isinstance(num_train_steps, int) or num_train_steps < 1:
            return JSONResponse({"error": "num_train_steps must be a positive integer"}, status_code=400)
        if dataset:
            if not valid_dataset_filename(dataset):
                return JSONResponse({"error": f"Invalid dataset '{dataset}': must be a plain *.jsonl file name"},
                                    status_code=400)
            await run_in_threadpool(reload_volumes, [DATA_DIR])
            if not (Path(DATA_DIR) / dataset).is_file():
                return JSONResponse({"error": f"Dataset '{dataset}' not found"}, status_code=400)
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

    @api.post("/datasets/upload")
    async def upload_dataset(request: Request):
        try:
            ds = TrainingDataset.model_validate(await request.json())
        except ValidationError:
            return JSONResponse({"error": "filename and content are required"}, status_code=400)
        if not ds.content:
            return JSONResponse({"error": "filename and content are required"}, status_code=400)

        if not valid_dataset_filename(ds.filename):
            return JSONResponse({"error": "filename must be a plain *.jsonl file name"}, status_code=400)

        payload = ds.payload()
        remote = Path(DATA_DIR) / ds.filename
        remote.parent.mkdir(parents=True, exist_ok=True)
        remote.write_bytes(payload)
        ds.size_bytes, ds.uploaded_at = len(payload), datetime.now()
        Path(REMOTE_METADATA_PATH).write_text(ds.model_dump_json(indent=2))
        await run_in_threadpool(commit_volumes, [DATA_DIR])
        return JSONResponse({"status": "success", "message": f"Training dataset {ds.filename} uploaded successfully",
                             "size_bytes": ds.size_bytes})

    @api.get("/datasets/list")
    async def list_datasets():
        data_dir = Path(DATA_DIR)
        if not data_dir.exists():
            return JSONResponse({"datasets": []})
        datasets = [TrainingDataset(filename=f.name, size_bytes=f.stat().st_size,
                                    modified_at=datetime.fromtimestamp(f.stat().st_mtime))
                    for f in data_dir.glob("*.jsonl")]
        meta_path = Path(REMOTE_METADATA_PATH)
        if meta_path.exists():
            meta = TrainingDataset.model_validate_json(meta_path.read_text())
            for d in datasets:
                if d.filename == meta.filename:
                    d.uploaded_at = meta.uploaded_at
        return JSONResponse({"datasets": [d.model_dump(mode="json") for d in datasets]})

    @api.get("/healthz")
    async def healthz():
        return JSONResponse({"status": "ok", "timestamp": datetime.now().isoformat()})

    return api
