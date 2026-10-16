"""Training progress reporting for live monitoring. Needs transformers; training images only.

Appends one JSON line per Trainer log to a file on the progress volume. The web API
(ai_web_core/web_api.py) reads the newest file and serves it at /get_training_progress.
"""
import json
import time
from pathlib import Path
from typing import Callable, Optional

from transformers import TrainerCallback

from .paths import PROGRESS_DIR, PROGRESS_FILE_TEMPLATE


class ProgressCallback(TrainerCallback):
    """Line format: {step, loss, learning_rate, epoch, timestamp, elapsed_seconds}.

    `commit_fn(paths)` persists the volume after each write; without it the web container
    (a separate container) would not see progress until the training container exits.
    """

    def __init__(self, commit_fn: Optional[Callable] = None):
        self.start_time = time.time()
        self.progress_file = PROGRESS_FILE_TEMPLATE.format(timestamp=int(self.start_time))
        self.commit_fn = commit_fn
        Path(self.progress_file).parent.mkdir(parents=True, exist_ok=True)
        # Old files are kept on purpose (accumulate); readers pick the newest.
        self._commit()

    def _commit(self):
        if self.commit_fn:
            self.commit_fn([PROGRESS_DIR])

    def on_log(self, args, state, control, logs=None, **kwargs):
        if logs is None:
            return
        entry = {
            "step": state.global_step,
            "loss": logs.get("loss"),
            "learning_rate": logs.get("learning_rate"),
            "epoch": logs.get("epoch"),
            "timestamp": time.time(),
            "elapsed_seconds": time.time() - self.start_time,
        }
        with open(self.progress_file, "a") as f:
            f.write(json.dumps(entry) + "\n")
        self._commit()
