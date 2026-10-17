"""Trained models: where a job's weights are saved and how they are described. No heavy imports, safe to load anywhere.

A training job saves its LoRA adapter (adapter_config.json, adapter_model.safetensors, the tokenizer files, ...) in a
directory of its own on the models volume: `trained/<job_id>`. When the job completes, its "completed" status event
carries the description built here (`describe`), which the Go server stores as a row of gb_trained_models:

    {"base_model", "model_size", "path", "files": [{"path", "size_bytes", "sha256"}], "size_bytes",
     "num_examples", "dataset_sha256", "params": {...}}

`path` is relative to the models volume; inference takes it as `model_path` (`resolve_model_dir`).
"""
import hashlib
import time
from pathlib import Path
from typing import Optional

from .paths import MODELS_DIR, TRAINED_MODEL_DIR_TEMPLATE, TRAINED_MODELS_SUBDIR
from .progress_events import valid_job_id

ADAPTER_CONFIG = "adapter_config.json"


def model_dir(job_id: str = "") -> str:
    """Absolute directory the weights of job `job_id` are saved in (a timestamp names a job that has no id)."""
    job_id = job_id or str(int(time.time()))
    if not valid_job_id(job_id):
        raise ValueError(f"invalid job id {job_id!r}")
    return TRAINED_MODEL_DIR_TEMPLATE.format(job_id=job_id)


def relative_path(directory: str, models_dir: str = MODELS_DIR) -> str:
    """`directory` relative to the models volume, with forward slashes: what is recorded and passed as model_path."""
    return Path(directory).relative_to(models_dir).as_posix()


def file_sha256(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def list_files(directory: str) -> list:
    """The files under `directory` (recursively, sorted): [{"path" (relative), "size_bytes", "sha256"}]."""
    root = Path(directory)
    return [{"path": p.relative_to(root).as_posix(), "size_bytes": p.stat().st_size, "sha256": file_sha256(p)}
            for p in sorted(root.rglob("*")) if p.is_file()]


def describe(directory: str, *, base_model: str, model_size: str, num_examples: int, dataset_path: Optional[str],
             params: dict, models_dir: str = MODELS_DIR) -> dict:
    """The description of a model saved in `directory`, as the "completed" event reports it (see the module comment)."""
    files = list_files(directory)
    return {
        "base_model": base_model,
        "model_size": model_size,
        "path": relative_path(directory, models_dir),
        "files": files,
        "size_bytes": sum(f["size_bytes"] for f in files),
        "num_examples": num_examples,
        "dataset_sha256": file_sha256(Path(dataset_path)) if dataset_path and Path(dataset_path).is_file() else "",
        "params": params,
    }


def valid_model_path(model_path: str) -> bool:
    """A model path is `trained/<job_id>` (what a job saved), nothing else: no absolute paths, no `..`."""
    parts = model_path.split("/") if isinstance(model_path, str) else []
    return len(parts) == 2 and parts[0] == TRAINED_MODELS_SUBDIR and valid_job_id(parts[1])


def resolve_model_dir(model_path: str, models_dir: str = MODELS_DIR) -> str:
    """Absolute directory of the model at `model_path` (relative to the models volume). Raises ValueError for a path
    that is not a trained model's, FileNotFoundError if the directory has no adapter."""
    if not valid_model_path(model_path):
        raise ValueError(f"invalid model_path {model_path!r}: expected '{TRAINED_MODELS_SUBDIR}/<job id>'")
    directory = Path(models_dir) / model_path
    if not (directory / ADAPTER_CONFIG).is_file():
        raise FileNotFoundError(f"No trained model at '{model_path}' (no {ADAPTER_CONFIG} in {directory}).")
    return str(directory)
