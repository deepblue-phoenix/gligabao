"""Provider-neutral configuration. No provider (modal/aws/...) imports here.

Everything can be overridden with env vars so you can change behaviour
without editing code while testing.
"""
import os
from dataclasses import dataclass, field
from pathlib import Path

from ai_train_core import paths
from ai_train_core.model_families import get_model_config


def _env(name: str, default: str) -> str:
    return os.environ.get(name, default)


AGENT_BINARY_NAME = "gligabao_agent_linux_amd64"


def _agent_binary() -> str:
    """The gligabao_agent binary (Linux, x86-64: what a Modal container runs) that is baked into the agent's image: SIDECAR_AGENT_BINARY,
    else the one next to the sidecar's code (the sidecar image has it there, see Dockerfile.sidecar), else the one `run_build.sh` builds
    in the repo's build/ directory. "" if there is none: the Modal app is then deployed without the ability to run research agents."""
    env = os.environ.get("SIDECAR_AGENT_BINARY")
    if env is not None:
        return env if os.path.isfile(env) else ""
    here = Path(__file__).resolve().parent                # sidecar/ai_train (/app/ai_train in the sidecar image)
    for candidate in (here.parent / AGENT_BINARY_NAME, here.parent.parent / "build" / AGENT_BINARY_NAME):
        if candidate.is_file():
            return str(candidate)
    return ""


@dataclass(frozen=True)
class TrainConfig:
    # Which compute provider runs the jobs: "modal" (only one for now).
    provider: str = field(default_factory=lambda: _env("SIDECAR_PROVIDER", "modal"))
    app_name: str = field(default_factory=lambda: _env("SIDECAR_APP_NAME", "gligabao-train"))

    # GPU type override for the train/inference jobs of every model size. Empty = per-size defaults
    # from the model family's model_config.py (ai_train_core/gemma4/; 4b: a10g, 12b: a100). Provider-specific names pass
    # straight through (e.g. "a10g", "a100" on Modal).
    gpu: str = field(default_factory=lambda: _env("SIDECAR_GPU", ""))
    train_timeout_s: int = field(default_factory=lambda: int(_env("SIDECAR_TRAIN_TIMEOUT_S", "7200")))

    # How long a GPU inference container, with the model loaded in it, stays up after its last request before it shuts
    # down (the next request then pays the model load again). Modal allows 2 s to 1200 s.
    infer_idle_timeout_s: int = field(default_factory=lambda: int(_env("SIDECAR_INFER_IDLE_TIMEOUT_S", "60")))

    # Optional secret holding HF_TOKEN etc. Empty = no secret attached.
    hf_secret: str = field(default_factory=lambda: _env("SIDECAR_HF_SECRET", ""))

    # The research agent (docs/agents/research.md, runner "modal"): the gligabao_agent binary runs in a Modal container. The binary is baked
    # into the image at deploy time ("" = none found: the app has no agent and POST /research/job_start answers 503). The secret holds
    # the keys the binary needs (ANTHROPIC_API_KEY, OPENAI_API_KEY; empty = no secret attached: the agent fails without keys).
    agent_binary: str = field(default_factory=_agent_binary)
    agent_secret: str = field(default_factory=lambda: _env("SIDECAR_AGENT_SECRET", ""))
    agent_timeout_s: int = field(default_factory=lambda: int(_env("SIDECAR_AGENT_TIMEOUT_S", "1800")))

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
