"""HTTP API in front of the training/inference functions. Provider neutral: no modal/aws imports.

Runs inside the web container. Everything provider-specific is injected as callables by
app.py (spawn a job, call a function, commit/reload volumes).

Endpoints:
    POST /start_training_job   {num_train_steps?, model_size?}  -> spawns a job, returns job_id
    GET  /get_training_progress                                  -> newest progress file's entries
    POST /generate_response    {prompt, max_tokens?, temperature?, model_size?}
    POST /add_to_eval_dataset  {prompt, response, notes?}
    GET  /get_eval_dataset_count
    POST /datasets/upload      {filename, content (base64 or plain jsonl)}   (alias: /upload_dataset)
    GET  /datasets/list                                                      (alias: /list_datasets)
    GET  /healthz
"""
import json
from datetime import datetime
from pathlib import Path
from typing import Callable

from fastapi import FastAPI, Request
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import JSONResponse
from pydantic import ValidationError

from api.models import TrainingDataset
from ai_train_core.model_config import MODEL_SIZES
from ai_train_core.paths import (DATA_DIR, EVAL_DATASET_PATH, EVAL_DIR, PROGRESS_DIR,
                                 REMOTE_DATASET_PATH, REMOTE_METADATA_PATH)


def create_api(
    spawn_training_job: Callable[..., str],     # (num_train_steps, model_size) -> job id
    generate_response: Callable[..., dict],     # (prompt, max_tokens, temperature, model_size) -> dict
    commit_volumes: Callable[[list], None],
    reload_volumes: Callable[[list], None],
) -> FastAPI:
    api = FastAPI(docs_url=None, redoc_url=None, openapi_url=None)

    def bad_model_size(model_size):
        if str(model_size).lower() not in MODEL_SIZES:
            return JSONResponse({"error": f"Invalid model_size '{model_size}'. Must be one of {list(MODEL_SIZES)}."},
                                status_code=400)

    @api.post("/start_training_job")
    async def start_training_job(request: Request):
        data = await request.json()
        num_train_steps = data.get("num_train_steps", 100)
        model_size = data.get("model_size", "4b")
        if (err := bad_model_size(model_size)):
            return err
        job_id = await run_in_threadpool(spawn_training_job, num_train_steps, model_size)
        return JSONResponse({"status": "started", "job_id": job_id,
                             "num_train_steps": num_train_steps, "model_size": model_size})

    @api.get("/get_training_progress")
    async def get_training_progress():
        # This container is long-lived, so its view of the volume can be stale: the training
        # container commits progress as it logs; reload to see it.
        await run_in_threadpool(reload_volumes, [PROGRESS_DIR])
        files = list(Path(PROGRESS_DIR).glob("training_progress_*.jsonl"))
        if not files:
            return JSONResponse({"progress": []})
        latest = max(files, key=lambda f: int(f.stem.split("_")[-1]))  # integer timestamp in the name
        with open(latest) as f:
            progress = [json.loads(line) for line in f if line.strip()]
        return JSONResponse({"progress": progress})

    @api.post("/generate_response")
    async def generate_response_endpoint(request: Request):
        data = await request.json()
        model_size = data.get("model_size", "4b")
        if (err := bad_model_size(model_size)):
            return err
        result = await run_in_threadpool(
            generate_response, data.get("prompt", ""), data.get("max_tokens", 1024),
            data.get("temperature", 0.7), model_size)
        return JSONResponse(result)

    @api.post("/add_to_eval_dataset")
    async def add_to_eval_dataset(request: Request):
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

    @api.get("/get_eval_dataset_count")
    async def get_eval_dataset_count():
        path = Path(EVAL_DATASET_PATH)
        count = 0
        if path.exists():
            with open(path) as f:
                count = sum(1 for _ in f)
        return JSONResponse({"count": count})

    @api.post("/datasets/upload")
    @api.post("/upload_dataset")
    async def upload_dataset(request: Request):
        try:
            ds = TrainingDataset.model_validate(await request.json())
        except ValidationError:
            return JSONResponse({"error": "filename and content are required"}, status_code=400)
        if not ds.content:
            return JSONResponse({"error": "filename and content are required"}, status_code=400)

        payload = ds.payload()
        remote = Path(REMOTE_DATASET_PATH)
        remote.parent.mkdir(parents=True, exist_ok=True)
        remote.write_bytes(payload)
        ds.size_bytes, ds.uploaded_at = len(payload), datetime.now()
        Path(REMOTE_METADATA_PATH).write_text(ds.model_dump_json(indent=2))
        await run_in_threadpool(commit_volumes, [DATA_DIR])
        return JSONResponse({"status": "success", "message": f"Training dataset {ds.filename} uploaded successfully",
                             "size_bytes": ds.size_bytes})

    @api.get("/datasets/list")
    @api.get("/list_datasets")
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
