"""Training progress reporting for live monitoring. Needs transformers; training images only.

Appends one JSON line per Trainer log to the job's file on the progress volume (see progress_events.py for the
format). The web API (api/api.py) serves it at /training/job_progress and streams it at /training/job_events.
"""
import time
from pathlib import Path
from typing import Callable, Optional

from transformers import TrainerCallback

from .progress_events import progress_path, write_event, write_status
from .paths import PROGRESS_DIR, PROGRESS_FILE_TEMPLATE


class ProgressCallback(TrainerCallback):
    """Progress events: {type: "progress", step, loss, learning_rate, epoch, timestamp, elapsed_seconds}, plus a
    "training" status event when training begins.

    `job_id` names the progress file (a timestamp if empty). `commit_fn(paths)` persists the volume after each
    write; without it the web container (a separate container) would not see progress until the training
    container exits.
    """

    def __init__(self, commit_fn: Optional[Callable] = None, job_id: str = ""):
        self.start_time = time.time()
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
        write_status(self.progress_file, "training", self.commit_fn, max_steps=state.max_steps)

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
