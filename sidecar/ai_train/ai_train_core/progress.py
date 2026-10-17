"""Training progress reporting for live monitoring. Needs transformers; training images only.

Appends one event per Trainer log to the job's EventLog (see progress_events.py for the format). The web API
(api/api.py) streams them at /training/job_events.
"""
import time
from typing import Optional

from transformers import TrainerCallback

from .event_log import EventLog
from .progress_events import JobCancelled, stop_requested, write_event, write_log, write_status


class ProgressCallback(TrainerCallback):
    """Progress events: {type: "progress", step, loss, learning_rate, epoch, timestamp, elapsed_seconds}, plus a
    "training" status event when training begins.

    `log` is the job's EventLog (None: nothing is reported, and no stop is looked for). Between steps (at most every
    `stop_check_s` seconds) the callback looks for a stop request and raises JobCancelled. A progress event that cannot
    be stored is skipped (training goes on); the log of the container still has it.
    Every log is also printed (the container's own log), with the time per step and the estimated time left.
    """

    def __init__(self, log: Optional[EventLog] = None, stop_check_s: float = 2.0):
        self.start_time = time.time()
        self.log = log
        self.stop_check_s = stop_check_s
        self._last_stop_check = 0.0

    def on_train_begin(self, args, state, control, **kwargs):
        self.start_time = time.time()  # the elapsed time of the events counts from here, not from the model load
        write_status(self.log, "training", max_steps=state.max_steps)
        print(f"[job] training begins: {state.max_steps} steps, logging every {args.logging_steps}", flush=True)

    def on_step_end(self, args, state, control, **kwargs):
        if self.log is None:
            return
        now = time.time()
        if now - self._last_stop_check < self.stop_check_s:
            return
        self._last_stop_check = now
        if stop_requested(self.log):
            write_log(self.log, f"stop requested: stopping after step {state.global_step}")
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
        write_event(self.log, entry, best_effort=True)
        elapsed, step = entry["elapsed_seconds"], state.global_step
        line = f"[job] step {step}/{state.max_steps} loss={entry['loss']} lr={entry['learning_rate']} {elapsed:.0f}s"
        if step > 0 and state.max_steps:
            line += f" ({elapsed / step:.1f}s/step, ~{elapsed / step * max(state.max_steps - step, 0):.0f}s left)"
        print(line, flush=True)
