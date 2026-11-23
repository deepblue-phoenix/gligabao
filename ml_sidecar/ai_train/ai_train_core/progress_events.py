"""The events of a training job. No heavy imports, safe to load anywhere.

The training container appends them to the job's EventLog (event_log.py: a key-value store of the provider, a Modal
Dict); the web API streams them to the Go server as SSE (api/api.py: /training/job_events). The index of an event in the
log is its SSE id, so a reader that was cut off can resume with `from`.

Events:
    {"type": "status", "status": "running" | "training" | "completed" | "failed" | "cancelled", "timestamp": ...,
     "max_steps"?: int, "final_loss"?: float, "error"?: str,
     "model"?: {...}}          # "completed" only: the weights the job saved (trained_model.py: describe)
    {"type": "progress", "step", "loss", "learning_rate", "epoch", "timestamp", "elapsed_seconds"}
    {"type": "log", "message": str, "timestamp": ...}        # what the job is doing (loading the model, ...)
"completed", "failed" and "cancelled" end the job.

Stopping a job: the web API sets a stop flag in the log (request_stop); the training container looks for it
(stop_requested) between steps and at the stages of the setup, and then ends the job with a "cancelled" status.

Writing is best effort for the events that only inform ("progress", "log": a failing store is logged and training goes
on) and not for the status events: the store is retried, and if it keeps failing the error is raised.
"""
import math
import re
import time
from typing import Optional

from .event_log import INFORMING_RETRIES, EventLog

TERMINAL_STATUSES = ("completed", "failed", "cancelled")

_JOB_ID_RE = re.compile(r"^[A-Za-z0-9_-]{1,64}$")


def valid_job_id(job_id: str) -> bool:
    """Job IDs are keys of the store, so only a conservative character set is accepted."""
    return isinstance(job_id, str) and _JOB_ID_RE.match(job_id) is not None


class JobCancelled(Exception):
    """Raised inside the training container when the job was asked to stop."""


def request_stop(log: EventLog) -> None:
    """Ask the job to stop (web API side)."""
    log.request_stop()


def stop_requested(log: Optional[EventLog]) -> bool:
    """Whether the job was asked to stop (training container side). A store that cannot be reached counts as no."""
    if log is None:
        return False
    try:
        # Looked for every couple of seconds between steps: an unreachable store must not hold the training up (see
        # event_log.INFORMING_RETRIES), and "cannot tell" counts as "no" anyway.
        return log.stop_requested(retries=INFORMING_RETRIES)
    except Exception as e:
        print(f"[job] could not look for a stop request: {e!r}", flush=True)
        return False


def _clean(v):
    """Non-finite floats (a diverged loss is NaN) are not valid JSON; the Go side could not decode them. Nested values
    (the "model" of a completed event, its params) are cleaned too: one NaN there would make EventLog.append raise
    (json.dumps(allow_nan=False)) and the "completed" event would be lost."""
    if isinstance(v, float) and not math.isfinite(v):
        return None
    if isinstance(v, dict):
        return {k: _clean(x) for k, x in v.items()}
    if isinstance(v, (list, tuple)):
        return [_clean(x) for x in v]
    return v


def write_event(log: Optional[EventLog], event: dict, best_effort: bool = False) -> None:
    """Append `event` (with a timestamp, if it has none) to the job's log. `best_effort`: a failure is only printed."""
    if log is None:
        return
    event = {k: _clean(v) for k, v in event.items()}
    event.setdefault("timestamp", time.time())
    try:
        # best effort: a short back-off (see event_log.INFORMING_RETRIES), so a store that is down skips events, not stalls steps
        log.append(event, retries=INFORMING_RETRIES if best_effort else None)
    except Exception as e:
        if not best_effort:
            raise
        print(f"[job] could not record an event: {e!r}", flush=True)


def write_status(log: Optional[EventLog], status: str, **fields) -> None:
    write_event(log, {"type": "status", "status": status, **fields})


def write_log(log: Optional[EventLog], message: str) -> None:
    """A "log" event: a line about what the job is doing. Also printed, for the container's own log."""
    print(f"[job] {message}", flush=True)
    write_event(log, {"type": "log", "message": message}, best_effort=True)


def is_terminal(event: dict) -> bool:
    return event.get("type") == "status" and event.get("status") in TERMINAL_STATUSES
