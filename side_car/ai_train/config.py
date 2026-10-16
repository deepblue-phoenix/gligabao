"""Provider-neutral configuration. No provider (modal/aws/...) imports here.

Everything can be overridden with env vars so you can change behaviour
without editing code while testing.
"""
import os
from dataclasses import dataclass, field


def _env(name: str, default: str) -> str:
    return os.environ.get(name, default)


@dataclass(frozen=True)
class TrainConfig:
    # Which compute provider runs the jobs: "modal" (only one for now).
    provider: str = field(default_factory=lambda: _env("SIDECAR_PROVIDER", "modal"))
    app_name: str = field(default_factory=lambda: _env("SIDECAR_APP_NAME", "gligabao-train"))

    # GPU type for the train job. Empty = no GPU (cheap, good while testing the bootstrap).
    # Provider-specific names pass straight through for now (e.g. "a10g", "a100" on Modal).
    gpu: str = field(default_factory=lambda: _env("SIDECAR_GPU", ""))
    train_timeout_s: int = field(default_factory=lambda: int(_env("SIDECAR_TRAIN_TIMEOUT_S", "7200")))

    # Optional secret holding HF_TOKEN etc. Empty = no secret attached.
    hf_secret: str = field(default_factory=lambda: _env("SIDECAR_HF_SECRET", ""))

    # Persistent storage: logical mount path in the container -> volume name.
    volumes: dict = field(default_factory=lambda: {
        "/data": _env("SIDECAR_VOL_DATA", "gligabao-train-data"),          # datasets
        "/models": _env("SIDECAR_VOL_MODELS", "gligabao-train-models"),    # fine-tuned outputs
        "/progress": _env("SIDECAR_VOL_PROGRESS", "gligabao-train-progress"),
    })


CONFIG = TrainConfig()
