"""System events of a training job: what happens around the training, not in it. No heavy imports, safe to load anywhere.

They go to the job's EventLog like the other events (progress_events.py) as
    {"type": "system", "event": "<subject>.<what>", "message": str, "data": {...}, "timestamp": ...}
and the Go server turns each one into a provider event (Hooks.ProviderEvents, kind "system", type "training_job.<event>"), which
the embedding program can store. Names (all best effort: a store that fails skips the event):

    container.started     the job's container is running: container_id, region, cloud, gpu, python, image_id, `start`
                          ("cold": a new container; "warm": a container that ran a job already and was reused)
    image.ready           the image the container runs from (it is built at deploy time and cached by the provider, so a job
                          never builds it: image_id, `cached` is always true here)
    gpu.info              the GPU of the container: name, memory_total_mb, driver
    libraries.importing / libraries.imported   the training libraries are imported (slow in a cold container; imported: seconds)
    dataset.loading / dataset.loaded       the dataset file is read (loaded: path, examples, size_bytes, seconds)
    model.loading / model.loaded           the base model is loaded (model_name, model_size, 4bit; loaded: seconds, gpu_memory_mb)
    lora.adding / lora.added               LoRA adapters are attached (rank, alpha)
    trainer.building / trainer.ready       the trainer is built (batch size, accumulation steps; ready: seconds)
    training.started      the first step may run (max_steps): the Trainer's own "training" status follows
    adapters.saving / adapters.saved       the adapters are written and the models volume committed (path, seconds)
    job.stop_observed     the container saw the stop request (where)
"""
import os
import platform
import shutil
import subprocess
import time
from typing import Optional

from .event_log import EventLog
from .progress_events import write_event

# Set by the first job of a container: a later job in the same container is a "warm" start.
_container_used = False


def system_event(log: Optional[EventLog], event: str, message: str = "", /, **data) -> None:
    """A "system" event `event` with `data` (None values are left out). Also printed, for the container's own log."""
    print(f"[job] system: {event} {message}".rstrip(), flush=True)
    write_event(log, {"type": "system", "event": event, "message": message,
                      "data": {k: v for k, v in data.items() if v is not None}}, best_effort=True)


def container_info(environ: Optional[dict] = None) -> dict:
    """What the container knows about itself (Modal sets MODAL_* variables in it); only the keys that are set."""
    env = os.environ if environ is None else environ
    info = {
        "container_id": env.get("MODAL_TASK_ID"),
        "region": env.get("MODAL_REGION"),
        "cloud": env.get("MODAL_CLOUD_PROVIDER"),
        "image_id": env.get("MODAL_IMAGE_ID"),
        "environment": env.get("MODAL_ENVIRONMENT"),
        "python": platform.python_version(),
        "hostname": platform.node(),
    }
    return {k: v for k, v in info.items() if v}


def gpu_info(run=subprocess.run) -> Optional[dict]:
    """{"name", "memory_total_mb", "driver"} of the first GPU, None when there is none (or nvidia-smi fails)."""
    if not shutil.which("nvidia-smi"):
        return None
    try:
        r = run(["nvidia-smi", "--query-gpu=name,memory.total,driver_version", "--format=csv,noheader,nounits"],
                capture_output=True, text=True, timeout=20)
        name, mem, driver = [p.strip() for p in r.stdout.strip().splitlines()[0].split(",")]
        return {"name": name, "memory_total_mb": int(float(mem)), "driver": driver}
    except Exception:
        return None


def gpu_memory_mb() -> Optional[int]:
    """Memory the torch allocator has in use on the GPU, in MB; None without torch or a GPU."""
    try:
        import torch
        return int(torch.cuda.memory_allocated() / 2 ** 20) if torch.cuda.is_available() else None
    except Exception:
        return None


def report_container_start(log: Optional[EventLog]) -> None:
    """The events of the container coming up: container.started (cold or warm), image.ready and gpu.info."""
    global _container_used
    start = "warm" if _container_used else "cold"
    _container_used = True
    info = container_info()
    system_event(log, "container.started",
                 "a new container started" if start == "cold" else "a container of an earlier job is reused",
                 start=start, **info)
    system_event(log, "image.ready", "the image is built and cached by the provider; the container started from it",
                 image_id=info.get("image_id"), cached=True)
    gpu = gpu_info()
    if gpu:
        system_event(log, "gpu.info", f"GPU {gpu['name']}", **gpu)


class Timer:
    """Seconds since it was made (a stage's duration)."""

    def __init__(self):
        self.t0 = time.time()

    def seconds(self) -> float:
        return round(time.time() - self.t0, 2)
