"""HTTP API in front of the training/inference functions. Provider neutral: no modal/aws imports.

Runs inside the web container. Everything provider-specific is injected as callables by
app.py (spawn a job, call a function, commit/reload volumes).

Endpoints:
    POST /training/job_start   {dataset, num_train_steps?, model_size?, dataset_content?}  -> spawns a job, returns a TrainingJob (api/models.py)
    GET  /training/job_progress?job_id=                          -> all the job's events so far, as {"progress": [...]}
    GET  /training/job_events?job_id=[&from=N]                   -> SSE stream of the job's progress events (see below)
    POST /training/job_cancel  {job_id}                          -> asks the job to stop (200 {job_id, status: "stopping"}); 404 unknown job, 409 already over
    POST /research/job_start   {topic, max_steps?, model?, dataset?}  -> spawns the research agent (the gligabao_agent binary, see
                                       ai_train_core/agent_run.py), returns {job_id, provider_job_id, status}; 503 if the app was deployed
                                       without the binary
    GET  /research/job_events?job_id=[&from=N]                   -> SSE stream of the research job's events (same protocol as the training job's)
    POST /research/job_cancel  {job_id}                          -> asks the job to stop (200, 404 unknown job, 409 already over)
    GET  /research/job_export?job_id=                            -> the file the agent handed back when its job ended (a ZIP archive,
                                       see internal/db/dbexport), streamed; 404 if the job has none. Its size and SHA-256 are in the
                                       "export" field of the job's final event
    POST /infer/query                  {prompt, max_tokens?, temperature?, model_size?, model_path?}
                                       model_path ("trained/<job id>", the path of a trained model that a completed job reported)
                                       picks the weights; model_size must be the size they were trained for
    POST /evals/add_to_dataset    {prompt, response, notes?}
    GET  /evals/get_dataset_count
    GET  /healthz

There are no dataset endpoints: training datasets are managed by the Go server (the dataset directories with a
dataset.toml manifest, see docs/training/datasets.md). The sidecar only stores a dataset internally: a job needs one, and a
job_start that carries `dataset_content` (base64 of the jsonl file) saves it on the data volume as `dataset` (the dataset's
name plus ".jsonl") before spawning the job; without `dataset_content`, `dataset` must already be on the volume. There is
no default dataset.

GET /training/job_events is Server-Sent Events: the events of the job's event log (ai_train_core/event_log.py, in a
key-value store of the provider: a Modal Dict) as they are written, each as `id: <index>`, `event: <status|progress|log>`,
`data: <the event as JSON>`. The log keeps all the events of the job (for 7 days on Modal), so a reader that was away
reconnects with `from=<last id + 1>` and gets what it missed. The stream ends after the "completed", "failed" or
"cancelled" event, or after `max_stream_s`. Comment lines (": keepalive") are sent while there is nothing new.

Watchdog: the training container reports its own end as an event, but it cannot when it is killed (out of memory, a
timeout, ...). While a stream has nothing new, every `watchdog_s` seconds it asks the provider about the job's call (its id
is in the log's meta, written by job_start); when the call is over and the log does not end with a terminal event, the
API writes the "failed" event itself. The training container is gone by then, so it is the only writer.

The research jobs use the same event logs, streams, cancel flag and watchdog (the events are those of
ai_train_core/agent_run.py, not the training job's).
"""
import asyncio
import json
import logging
import time
import uuid
from datetime import datetime
from pathlib import Path
from typing import Callable, Optional

from fastapi import FastAPI, Query, Request
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import JSONResponse, StreamingResponse

from api.models import TrainingDataset, TrainingJob
from ai_train_core.dataset import store_dataset, valid_dataset_filename
from ai_train_core.model_families import model_sizes
from ai_train_core.event_log import EventLog
from ai_train_core.paths import DATA_DIR, EVAL_DATASET_PATH, EVAL_DIR
from ai_train_core.progress_events import is_terminal, request_stop, valid_job_id, write_status
from ai_train_core.trained_model import valid_model_path

MODEL_SIZES = model_sizes()   # of the default model family

log = logging.getLogger("uvicorn.error")  # shows in the container's log next to the access lines


def _describe(ev: dict) -> str:
    """A one-line summary of a progress event, for the log."""
    if ev["type"] == "progress":
        return f"step {ev.get('step')} loss {ev.get('loss')}"
    if ev["type"] == "log":
        return str(ev.get("message"))
    if ev["type"] == "state":   # a research job
        return f"{ev.get('agent')}: {ev.get('state')}"
    if ev["type"] == "usage":
        return f"usage {ev.get('usage')}"
    return f"status {ev.get('status')}"


def create_api(
    spawn_training_job: Callable[..., str],     # (num_train_steps, model_size, dataset, job_id) -> the provider's call id
    infer_response: Callable[..., dict],     # (prompt, max_tokens, temperature, model_size, model_path) -> dict
    commit_volumes: Callable[[list], None],
    reload_volumes: Callable[[list], None],
    event_kv,                                   # the key-value store of the event logs (Provider.event_kv)
    call_status: Callable[[str], object],       # (the provider's call id) -> CallStatus (Provider.call_status)
    spawn_agent_job: Optional[Callable[..., str]] = None,   # (topic, max_steps, model, dataset, job_id) -> the provider's call id; None: no agent deployed
    poll_interval_s: float = 1.0,               # how often /training/job_events looks for new events
    max_stream_s: float = 300.0,                # how long one /training/job_events response lasts at most
    watchdog_s: float = 15.0,                   # how often an idle stream checks that the job's call is still alive
) -> FastAPI:
    api = FastAPI(docs_url=None, redoc_url=None, openapi_url=None)

    def events_of(job_id: str) -> EventLog:
        return EventLog(event_kv, job_id, retries=2)  # a reader that fails soon polls again; it does not wait

    def dead_job_event(events: EventLog, what: str = "training") -> bool:
        """Watchdog: if the job's call is over and the log has no terminal event, write the "failed" one. Whether it
        wrote one. `what` names the kind of job in the message."""
        meta = events.meta() or {}
        call_id = meta.get("call_id")
        if not call_id:
            return False
        status = call_status(call_id)
        if status.state == "running":
            return False
        # Look at the log only after the call is over: the container's last event comes before its end.
        last = events.last_event()
        if last is not None and is_terminal(last):
            return False
        error = status.error or "the job's function ended without reporting that the job is over"
        write_status(events, "failed", error=f"the {what} container ended without reporting it: {error}"[:2000])
        log.warning("%s job %s: its call ended (%s) with no final event: reported as failed", what, events.job_id, status.state)
        return True

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
        if not dataset:
            return JSONResponse({"error": "dataset is required: the file name of a training dataset on the data volume "
                                          "(<dataset name>.jsonl)"}, status_code=400)
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
        job_id = uuid.uuid4().hex[:16]  # names the event log; the provider's own id is only for its dashboard
        events = events_of(job_id)
        meta = {"created": time.time(), "model_size": model_size}
        await run_in_threadpool(events.set_meta, meta)  # the job exists (cancel, events) before its container does
        try:
            provider_job_id = await run_in_threadpool(spawn_training_job, num_train_steps, model_size, dataset, job_id)
        except Exception as e:
            # The log has meta but will never have a container or a call_id, so the watchdog (which needs the call_id)
            # could not tell that nothing is running. Close the log here: nobody else will. Best effort: the error is raised anyway.
            try:
                await run_in_threadpool(write_status, events, "failed", error=f"the job could not be started: {e!r}"[:2000])
            except Exception as we:
                log.warning("training job %s: could not record that it failed to start: %r", job_id, we)
            raise
        await run_in_threadpool(events.set_meta, {**meta, "call_id": str(provider_job_id)})  # for the watchdog
        job = TrainingJob(job_id=job_id, provider_job_id=str(provider_job_id), model_size=model_size,
                          num_train_steps=num_train_steps, dataset=dataset)
        return JSONResponse(job.model_dump(mode="json"))

    @api.get("/training/job_progress")
    async def job_progress(job_id: str = ""):
        if not valid_job_id(job_id):
            return JSONResponse({"error": "job_id is required (letters, digits, - and _)"}, status_code=400)
        events = await run_in_threadpool(events_of(job_id).read_from, 0)
        return JSONResponse({"progress": [ev for _, ev in events]})

    async def cancel_job(request: Request, what: str):
        """Ask a job (`what`: "training" or "research") to stop. The container sees the stop flag between steps (and between the
        stages of its setup) and ends the job with a "cancelled" event, so the stop takes effect at the next step, not instantly."""
        try:
            data = await request.json()
        except ValueError:
            data = None
        job_id = data.get("job_id", "") if isinstance(data, dict) else ""
        if not valid_job_id(job_id):
            return JSONResponse({"error": "job_id is required (letters, digits, - and _)"}, status_code=400)
        events = events_of(job_id)
        if not await run_in_threadpool(events.exists):
            return JSONResponse({"error": f"no such job {job_id!r}"}, status_code=404)
        last = await run_in_threadpool(events.last_event)
        if last is not None and is_terminal(last):
            return JSONResponse({"error": f"job {job_id!r} is already over"}, status_code=409)
        await run_in_threadpool(request_stop, events)
        log.info("%s job %s: stop requested", what, job_id)
        return JSONResponse({"job_id": job_id, "status": "stopping"})

    @api.post("/training/job_cancel")
    async def job_cancel(request: Request):
        return await cancel_job(request, "training")

    @api.post("/research/job_cancel")
    async def research_job_cancel(request: Request):
        return await cancel_job(request, "research")

    def events_stream(request: Request, job_id: str, start: int, what: str):
        """The SSE response of the events of a job (`what`: "training" or "research", for the log and the watchdog's message)."""
        if not valid_job_id(job_id):
            return JSONResponse({"error": "job_id is required (letters, digits, - and _)"}, status_code=400)
        events = events_of(job_id)

        async def stream():
            pos = start
            now = time.monotonic()
            deadline, next_watch = now + max_stream_s, now + watchdog_s
            log.info("%s job %s: event stream opened from %d", what, job_id, start)
            yield ": connected\n\n"
            polls = 0
            while time.monotonic() < deadline:
                if await request.is_disconnected():
                    log.info("%s job %s: event stream closed by the reader at %d", what, job_id, pos)
                    return
                try:
                    new = await run_in_threadpool(events.read_from, pos)
                except Exception as e:  # e.g. the store is unreachable: try again next time
                    log.warning("%s job %s: reading the event log failed: %r", what, job_id, e)
                    new = []
                polls += 1
                for seq, ev in new:
                    yield f"id: {seq}\nevent: {ev['type']}\ndata: {json.dumps(ev)}\n\n"
                    log.info("%s job %s: event %d sent: %s", what, job_id, seq, _describe(ev))
                    pos = seq + 1
                    next_watch = time.monotonic() + watchdog_s
                    if is_terminal(ev):
                        return
                if not new:
                    yield ": keepalive\n\n"
                    if polls % 30 == 0:
                        log.info("%s job %s: nothing new after event %d", what, job_id, pos - 1)
                    if time.monotonic() >= next_watch:
                        next_watch = time.monotonic() + watchdog_s
                        try:
                            if await run_in_threadpool(dead_job_event, events, what):
                                continue  # read the event it wrote right away
                        except Exception as e:
                            log.warning("%s job %s: checking the job's call failed: %r", what, job_id, e)
                await asyncio.sleep(poll_interval_s)
            log.info("%s job %s: event stream ends at its time limit (the reader reconnects from %d)", what, job_id, pos)

        return StreamingResponse(stream(), media_type="text/event-stream",
                                 headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})

    @api.get("/training/job_events")
    async def job_events(request: Request, job_id: str = "", start: int = Query(0, alias="from", ge=0)):
        return events_stream(request, job_id, start, "training")

    @api.get("/research/job_events")
    async def research_job_events(request: Request, job_id: str = "", start: int = Query(0, alias="from", ge=0)):
        return events_stream(request, job_id, start, "research")

    @api.get("/research/job_export")
    async def research_job_export(job_id: str = ""):
        if not valid_job_id(job_id):
            return JSONResponse({"error": "job_id is required (letters, digits, - and _)"}, status_code=400)
        events = events_of(job_id)
        meta = await run_in_threadpool(events.export_meta)
        if not isinstance(meta, dict) or not meta.get("chunks"):
            return JSONResponse({"error": f"job {job_id!r} has no export"}, status_code=404)

        async def stream():
            for n in range(meta["chunks"]):
                chunk = await run_in_threadpool(events.read_export_chunk, n)
                if chunk is None:   # the store lost it: the answer breaks off, and the reader sees the size/SHA-256 not match
                    log.error("research job %s: chunk %d of the export is missing", job_id, n)
                    raise RuntimeError(f"chunk {n} of the export of job {job_id} is missing")
                yield chunk
            log.info("research job %s: export sent (%d bytes)", job_id, meta.get("bytes", 0))

        headers = {"Content-Length": str(meta["bytes"])} if isinstance(meta.get("bytes"), int) else {}
        return StreamingResponse(stream(), media_type="application/zip", headers=headers)

    @api.post("/research/job_start")
    async def research_job_start(request: Request):
        try:
            data = await request.json()
        except ValueError:
            data = None
        if not isinstance(data, dict):
            return JSONResponse({"error": "the body must be a JSON object"}, status_code=400)
        topic = data.get("topic")
        max_steps = data.get("max_steps")
        if max_steps is None:
            max_steps = 20
        model = data.get("model") or ""
        dataset = data.get("dataset") or ""
        if not isinstance(topic, str) or not topic.strip():
            return JSONResponse({"error": "topic is required"}, status_code=400)
        if len(topic) > 8000:
            return JSONResponse({"error": "topic is too long"}, status_code=400)
        if isinstance(max_steps, bool) or not isinstance(max_steps, int) or not 1 <= max_steps <= 40:
            return JSONResponse({"error": "max_steps must be an integer from 1 to 40"}, status_code=400)
        if not isinstance(model, str) or len(model) > 100 or any(c.isspace() for c in model):
            return JSONResponse({"error": "model is not a valid model name"}, status_code=400)
        if not isinstance(dataset, str) or len(dataset) > 100 or any(c.isspace() for c in dataset):
            return JSONResponse({"error": "dataset is not a valid dataset name"}, status_code=400)
        if spawn_agent_job is None:
            return JSONResponse({"error": "this Modal app has no research agent: build the gligabao_agent binary for Linux "
                                          "(./run_build.sh makes build/gligabao_agent_linux_amd64) and deploy the app again"},
                                status_code=503)
        job_id = uuid.uuid4().hex[:16]  # names the event log
        events = events_of(job_id)
        meta = {"created": time.time(), "kind": "research"}
        await run_in_threadpool(events.set_meta, meta)  # the job exists (cancel, events) before its container does
        try:
            provider_job_id = await run_in_threadpool(spawn_agent_job, topic.strip(), max_steps, model, dataset, job_id)
        except Exception as e:
            try:
                await run_in_threadpool(write_status, events, "failed", error=f"the job could not be started: {e!r}"[:2000])
            except Exception as we:
                log.warning("research job %s: could not record that it failed to start: %r", job_id, we)
            raise
        await run_in_threadpool(events.set_meta, {**meta, "call_id": str(provider_job_id)})  # for the watchdog
        log.info("research job %s: started", job_id)
        return JSONResponse({"job_id": job_id, "provider_job_id": str(provider_job_id), "status": "started"})

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
