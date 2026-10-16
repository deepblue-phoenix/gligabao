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
from .progress_events import progress_path, write_status


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
              dataset: str = "", job_id: str = "") -> dict:
    """Fine-tune `model_size` on `dataset` (a file name on the data volume; the default dataset if empty).
    Must run in the Unsloth GPU image.

    Reports to the job's progress file (progress_events.py): "running" at the start, the Trainer's progress while
    training, then "completed" (with the final loss) or "failed" (with the error, which is re-raised).
    """
    job_id = job_id or str(int(time.time()))
    path = progress_path(job_id)
    write_status(path, "running", commit_fn, model_size=model_size, num_train_steps=num_train_steps)
    try:
        dataset_path = resolve_dataset_path(dataset)
        from . import model_train
        result = model_train.run_finetuning(num_train_steps=num_train_steps, model_size=model_size,
                                            commit_fn=commit_fn, dataset_path=dataset_path, job_id=job_id)
    except BaseException as e:
        # The traceback goes into the event too (the Go server logs it) and, by the re-raise, into the container's log.
        write_status(path, "failed", commit_fn, error=f"{type(e).__name__}: {e}"[:2000],
                     traceback="".join(traceback.format_exception(e))[-6000:])
        raise
    write_status(path, "completed", commit_fn, final_loss=result.get("final_loss"))
    return result
