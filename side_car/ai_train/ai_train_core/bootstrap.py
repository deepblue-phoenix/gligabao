"""Code that runs INSIDE the remote container. Provider neutral: no modal/aws imports.

app.py only wires these up; the training pipelines live in model_train.py (4B, Unsloth) and
model_train_gemma4_12b.py (12B, plain transformers). Those are imported lazily because each
needs a different image.
"""
import os
import platform
import shutil
import subprocess
from typing import Callable, Optional

from .model_config import get_model_config


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


def run_train(num_train_steps: int, model_size: str, commit_fn: Optional[Callable] = None) -> dict:
    """Fine-tune `model_size`. Must run in the image matching the model's backend."""
    backend = get_model_config(model_size)["backend"]
    if backend == "unsloth":
        from . import model_train
        return model_train.run_finetuning(num_train_steps=num_train_steps, model_size=model_size,
                                          commit_fn=commit_fn)
    from . import model_train_gemma4_12b
    return model_train_gemma4_12b.run_finetuning_gemma4_12b(num_train_steps=num_train_steps, commit_fn=commit_fn)
