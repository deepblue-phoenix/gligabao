"""Provider-neutral configuration. No provider (modal/aws/...) imports here.

Everything can be overridden with env vars so you can change behaviour
without editing code while testing.
"""
import os
from dataclasses import dataclass, field

from ai_train_core import paths
from ai_train_core.model_config import get_model_config


def _env(name: str, default: str) -> str:
    return os.environ.get(name, default)


@dataclass(frozen=True)
class TrainConfig:
    # Which compute provider runs the jobs: "modal" (only one for now).
    provider: str = field(default_factory=lambda: _env("SIDECAR_PROVIDER", "modal"))
    app_name: str = field(default_factory=lambda: _env("SIDECAR_APP_NAME", "gligabao-train"))

    # GPU type override for the train/inference jobs of every model size. Empty = per-size defaults
    # from ai_train_core/model_config.py (4b: a10g, 12b: a100). Provider-specific names pass
    # straight through (e.g. "a10g", "a100" on Modal).
    gpu: str = field(default_factory=lambda: _env("SIDECAR_GPU", ""))
    train_timeout_s: int = field(default_factory=lambda: int(_env("SIDECAR_TRAIN_TIMEOUT_S", "7200")))

    # How long a GPU inference container, with the model loaded in it, stays up after its last request before it shuts
    # down (the next request then pays the model load again). Modal allows 2 s to 1200 s.
    infer_idle_timeout_s: int = field(default_factory=lambda: int(_env("SIDECAR_INFER_IDLE_TIMEOUT_S", "300")))

    # Optional secret holding HF_TOKEN etc. Empty = no secret attached.
    hf_secret: str = field(default_factory=lambda: _env("SIDECAR_HF_SECRET", ""))

    # Persistent storage: logical mount path in the container -> volume name.
    volumes: dict = field(default_factory=lambda: {
        paths.DATA_DIR: _env("SIDECAR_VOL_DATA", "gligabao-train-data"),          # datasets
        paths.MODELS_DIR: _env("SIDECAR_VOL_MODELS", "gligabao-train-models"),    # fine-tuned adapters
        paths.EVAL_DIR: _env("SIDECAR_VOL_EVAL", "gligabao-train-eval"),          # eval examples
        paths.HF_CACHE_DIR: _env("SIDECAR_VOL_HF_CACHE", "gligabao-train-hf-cache"),  # base-model cache
    })

    def gpu_for(self, model_size: str) -> str:
        """GPU for a model size: the SIDECAR_GPU override, else the size's default."""
        return self.gpu or get_model_config(model_size)["default_gpu"]


CONFIG = TrainConfig()
