"""The progress file of a training job: one JSON event per line. No heavy imports, safe to load anywhere.

The training container appends events (progress.py, bootstrap.py); the web API tails the file and streams the
events to the Go server as SSE (api/api.py: /training/job_events). The index of an event in the file (counting
complete, valid lines only) is its SSE id, so a reader that was cut off can resume with `from`.

Events:
    {"type": "status", "status": "running" | "training" | "completed" | "failed" | "cancelled", "timestamp": ...,
     "max_steps"?: int, "final_loss"?: float, "error"?: str}
    {"type": "progress", "step", "loss", "learning_rate", "epoch", "timestamp", "elapsed_seconds"}
    {"type": "log", "message": str, "timestamp": ...}        # what the job is doing (loading the model, ...)
A line without "type" (written before this field existed) is a progress event. "completed", "failed" and "cancelled"
end the job.

Stopping a job: the web container writes a stop file next to the progress file (request_stop); the training container
looks for it (stop_requested) between steps and at the stages of the setup, and then ends the job with a "cancelled"
status. The training container is the only writer of the progress file (concurrent writers to one file of a volume would
overwrite each other's commits); the stop file is written by the web container alone.
"""
import json
import math
import re
import time
from pathlib import Path
from typing import Callable, Optional

from .paths import PROGRESS_DIR, PROGRESS_FILE_TEMPLATE

TERMINAL_STATUSES = ("completed", "failed", "cancelled")

_JOB_ID_RE = re.compile(r"^[A-Za-z0-9_-]{1,64}$")


def valid_job_id(job_id: str) -> bool:
    """Job IDs name files, so only a conservative character set is accepted."""
    return isinstance(job_id, str) and _JOB_ID_RE.match(job_id) is not None


def progress_path(job_id: str) -> str:
    if not valid_job_id(job_id):
        raise ValueError(f"invalid job id {job_id!r}")
    return PROGRESS_FILE_TEMPLATE.format(timestamp=job_id)


class JobCancelled(Exception):
    """Raised inside the training container when the job was asked to stop."""


def stop_path(job_id: str) -> str:
    return progress_path(job_id).replace(".jsonl", ".stop")


def request_stop(job_id: str, commit_fn: Optional[Callable] = None) -> None:
    """Ask the job to stop (web container side): writes the stop file and persists the volume."""
    path = Path(stop_path(job_id))
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(str(time.time()))
    if commit_fn:
        commit_fn([PROGRESS_DIR])


def stop_requested(job_id: str, reload_fn: Optional[Callable] = None) -> bool:
    """Whether the job was asked to stop (training container side). `reload_fn(paths)` refreshes the volume first, as
    the stop file is written by another container."""
    if reload_fn:
        reload_fn([PROGRESS_DIR])
    return Path(stop_path(job_id)).exists()


def _clean(v):
    """Non-finite floats (a diverged loss is NaN) are not valid JSON; the Go side could not decode them."""
    if isinstance(v, float) and not math.isfinite(v):
        return None
    return v


def write_event(path: str, event: dict, commit_fn: Optional[Callable] = None) -> None:
    """Append `event` (with a timestamp, if it has none) to the progress file and persist the volume."""
    event = {k: _clean(v) for k, v in event.items()}
    event.setdefault("timestamp", time.time())
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    with open(path, "a") as f:
        f.write(json.dumps(event, allow_nan=False) + "\n")
    if commit_fn:
        commit_fn([PROGRESS_DIR])


def write_status(path: str, status: str, commit_fn: Optional[Callable] = None, **fields) -> None:
    write_event(path, {"type": "status", "status": status, **fields}, commit_fn)


def write_log(path: str, message: str, commit_fn: Optional[Callable] = None) -> None:
    """A "log" event: a line about what the job is doing. Also printed, for the container's own log."""
    print(f"[job] {message}", flush=True)
    write_event(path, {"type": "log", "message": message}, commit_fn)


def read_events(path: str) -> list:
    """All complete events of the file, each with a "type". A line still being written (no newline yet) and
    lines that are not JSON objects are skipped, deterministically, so event indexes stay stable between reads."""
    p = Path(path)
    if not p.exists():
        return []
    text = p.read_text()
    lines = text.split("\n")
    lines.pop()  # whatever follows the last newline: "" or a partial line
    events = []
    for line in lines:
        if not line.strip():
            continue
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(ev, dict):
            ev.setdefault("type", "progress")
            events.append(ev)
    return events


def is_terminal(event: dict) -> bool:
    return event.get("type") == "status" and event.get("status") in TERMINAL_STATUSES
