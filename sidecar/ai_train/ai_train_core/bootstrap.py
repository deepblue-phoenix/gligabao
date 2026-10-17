"""Code that runs INSIDE the remote container. Provider neutral: no modal/aws imports.

app.py only wires these up; the training pipeline lives in model_train.py (Unsloth), which is
imported lazily because only the GPU image has it.
"""
import os
import platform
import shutil
import subprocess
import time
import traceback
from typing import Callable, Optional

from .dataset import resolve_dataset_path
from .event_log import EventLog
from .progress_events import JobCancelled, write_log, write_status


def run_ping(volume_paths: list) -> dict:
    """Report what the container looks like. Used to verify the bootstrap end to end."""
    gpu = None
    if shutil.which("nvidia-smi"):
        r = subprocess.run(["nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader"],
                           capture_output=True, text=True)
        gpu = r.stdout.strip() or r.stderr.strip()
    return {
        "python": platform.python_version(),
        "gpu": gpu,
        "volumes": {p: os.path.isdir(p) for p in volume_paths},
        "hf_token_set": bool(os.environ.get("HF_TOKEN")),
    }


def run_train(num_train_steps: int, model_size: str, commit_fn: Optional[Callable] = None,
              dataset: str = "", job_id: str = "", event_kv=None) -> dict:
    """Fine-tune `model_size` on `dataset` (a file name on the data volume; the default dataset if empty).
    Must run in the Unsloth GPU image.

    Reports to the job's event log (event_log.py, in the key-value store `event_kv`; None: nothing is reported):
    "running" at the start, the Trainer's progress while training, then "completed" (with the final loss and the
    description of the saved weights, trained_model.py), "cancelled" (a stop was requested: see progress_events.py; no
    error) or "failed" (with the error, which is re-raised). `commit_fn(paths)` persists the models volume.
    """
    job_id = job_id or str(int(time.time()))
    log = EventLog(event_kv, job_id) if event_kv is not None else None
    write_status(log, "running", model_size=model_size, num_train_steps=num_train_steps)
    try:
        dataset_path = resolve_dataset_path(dataset)
        write_log(log, f"importing the training libraries; dataset {dataset or '(default)'}")
        from . import model_train
        result = model_train.run_finetuning(num_train_steps=num_train_steps, model_size=model_size,
                                            commit_fn=commit_fn, dataset_path=dataset_path, job_id=job_id, events=log)
    except JobCancelled as e:
        write_status(log, "cancelled", error=str(e))
        return {"status": "cancelled", "error": str(e)}
    except BaseException as e:
        # The traceback goes into the event too (the Go server logs it) and, by the re-raise, into the container's log.
        try:
            write_status(log, "failed", error=f"{type(e).__name__}: {e}"[:2000],
                         traceback="".join(traceback.format_exception(e))[-6000:])
        except Exception as we:  # the job's call fails anyway, which the web API's watchdog reports
            print(f"[job] could not record the failure: {we!r}", flush=True)
        raise
    # The completed event carries the description of the weights the job saved: the Go server records them as a trained model.
    write_status(log, "completed", final_loss=result.get("final_loss"), model=result.get("model"))
    return result
