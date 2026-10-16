"""Training progress reporting for live monitoring. Needs transformers; training images only.

Appends one JSON line per Trainer log to the job's file on the progress volume (see progress_events.py for the
format). The web API (api/api.py) serves it at /training/job_progress and streams it at /training/job_events.
"""
import time
from pathlib import Path
from typing import Callable, Optional

from transformers import TrainerCallback

from .progress_events import JobCancelled, progress_path, stop_requested, write_event, write_log, write_status
from .paths import PROGRESS_DIR, PROGRESS_FILE_TEMPLATE


class ProgressCallback(TrainerCallback):
    """Progress events: {type: "progress", step, loss, learning_rate, epoch, timestamp, elapsed_seconds}, plus a
    "training" status event when training begins.

    `job_id` names the progress file (a timestamp if empty). `commit_fn(paths)` persists the volume after each
    write; without it the web container (a separate container) would not see progress until the training
    container exits.

    `reload_fn(paths)` refreshes the volume, to see the stop file that the web container writes (see progress_events.py):
    between steps (at most every `stop_check_s` seconds) the callback looks for it and raises JobCancelled.
    Every log is also printed (the container's own log), with the time per step and the estimated time left.
    """

    def __init__(self, commit_fn: Optional[Callable] = None, job_id: str = "", reload_fn: Optional[Callable] = None,
                 stop_check_s: float = 2.0):
        self.start_time = time.time()
        self.job_id = job_id
        self.reload_fn = reload_fn
        self.stop_check_s = stop_check_s
        self._last_stop_check = 0.0
        self.progress_file = (progress_path(job_id) if job_id
                              else PROGRESS_FILE_TEMPLATE.format(timestamp=int(self.start_time)))
        self.commit_fn = commit_fn
        Path(self.progress_file).parent.mkdir(parents=True, exist_ok=True)
        # Old files are kept on purpose (accumulate); readers pick the newest.
        self._commit()

    def _commit(self):
        if self.commit_fn:
            self.commit_fn([PROGRESS_DIR])

    def on_train_begin(self, args, state, control, **kwargs):
        self.start_time = time.time()  # the elapsed time of the events counts from here, not from the model load
        write_status(self.progress_file, "training", self.commit_fn, max_steps=state.max_steps)
        print(f"[job] training begins: {state.max_steps} steps, logging every {args.logging_steps}", flush=True)

    def on_step_end(self, args, state, control, **kwargs):
        if not self.job_id:
            return
        now = time.time()
        if now - self._last_stop_check < self.stop_check_s:
            return
        self._last_stop_check = now
        if stop_requested(self.job_id, self.reload_fn):
            write_log(self.progress_file, f"stop requested: stopping after step {state.global_step}", self.commit_fn)
            raise JobCancelled(f"stopped at step {state.global_step}")

    def on_log(self, args, state, control, logs=None, **kwargs):
        if logs is None:
            return
        entry = {
            "type": "progress",
            "step": state.global_step,
            "loss": logs.get("loss"),
            "learning_rate": logs.get("learning_rate"),
            "epoch": logs.get("epoch"),
            "timestamp": time.time(),
            "elapsed_seconds": time.time() - self.start_time,
        }
        write_event(self.progress_file, entry, self.commit_fn)
        elapsed, step = entry["elapsed_seconds"], state.global_step
        line = f"[job] step {step}/{state.max_steps} loss={entry['loss']} lr={entry['learning_rate']} {elapsed:.0f}s"
        if step > 0 and state.max_steps:
            line += f" ({elapsed / step:.1f}s/step, ~{elapsed / step * max(state.max_steps - step, 0):.0f}s left)"
        print(line, flush=True)
