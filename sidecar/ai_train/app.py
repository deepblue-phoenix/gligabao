"""Training bootstrap, provider-neutral wiring.

Run (Modal):
    cd sidecar/ai_train
    modal run app.py                                          # ping: checks image/volumes
    modal run app.py --action sync                            # upload the local dataset to the data volume
    modal run app.py --action validate                        # validate the remote dataset
    modal run app.py --action train --train-steps 100 --model-size 4b      # 4b (A10G) or 12b (A100)
                                                              # [--dataset my.jsonl: a file on the data volume]
    modal run app.py --action generate --model-size 4b --prompt "..."      # fine-tuned model inference
                                                              # [--model-path trained/<job id>: a trained model; default: the legacy adapter]
    modal serve app.py                                        # web API (dev); `modal deploy app.py` to persist

Cloud-specific code lives in providers/. This file only declares *what* to run.

Two training images exist because of Gemma-4's architectures (see ai_train_core/model_config.py):
Both sizes use Unsloth (requirements_train.txt, one shared image).
"""
import os
import sys

modd_str = os.path.abspath(os.path.dirname(__file__))
# Local: this dir. In the container: the mounted code dir (see providers/modal_provider.py).
for _p in (modd_str, "/pkg"):
    if _p not in sys.path:
        sys.path.insert(0, _p)

from ai_train_core import paths
from ai_train_core.model_config import MODEL_SIZES
from config import CONFIG
from providers import FunctionSpec, ImageSpec, get_provider
from training_data import get_local_paths

provider = get_provider(
    CONFIG.provider,
    app_name=CONFIG.app_name,
    local_pkg_dir=modd_str,
    volumes=CONFIG.volumes,
)
# Provider CLIs (e.g. `modal run`) look for this module-level object.
app = provider.native_app

secrets = (CONFIG.hf_secret,) if CONFIG.hf_secret else ()
ALL_VOLUMES = CONFIG.volumes
VOLUME_PATHS = tuple(ALL_VOLUMES)


def _volumes(*mount_paths: str) -> dict:
    return {p: ALL_VOLUMES[p] for p in mount_paths}


# Volume sets per kind of function.
DATA_VOLS = _volumes(paths.DATA_DIR)
TRAIN_VOLS = _volumes(paths.DATA_DIR, paths.MODELS_DIR, paths.PROGRESS_DIR, paths.HF_CACHE_DIR)
INFER_VOLS = _volumes(paths.MODELS_DIR, paths.HF_CACHE_DIR)
WEB_VOLS = _volumes(paths.DATA_DIR, paths.EVAL_DIR, paths.PROGRESS_DIR)

# Images. Code is mounted last, so code edits don't rebuild the pip layers.
CPU_IMAGE = ImageSpec(gpu=False)
WEB_IMAGE = ImageSpec(gpu=False, requirements_path=f"{modd_str}/requirements_web.txt")
UNSLOTH_IMAGE = ImageSpec(gpu=True, requirements_path=f"{modd_str}/requirements_train.txt")             # 4B and 12B


#-----------------------------------------------------------------------------------------
# CHECKS / DATASET

@provider.function(FunctionSpec(image=CPU_IMAGE, timeout_s=300,
                                volumes=ALL_VOLUMES, secrets=secrets))
def ping() -> dict:
    from ai_train_core import bootstrap
    return bootstrap.run_ping(list(VOLUME_PATHS))


@provider.function(FunctionSpec(image=CPU_IMAGE, timeout_s=300, volumes=DATA_VOLS))
def sync_dataset_to_volume(dataset_content: bytes, local_mtime: float) -> dict:
    """Write the dataset to the data volume if the local copy is newer."""
    from ai_train_core import dataset
    return dataset.sync_dataset(dataset_content, local_mtime, commit_fn=provider.commit_volumes)


@provider.function(FunctionSpec(image=CPU_IMAGE, timeout_s=300, volumes=DATA_VOLS))
def validate_dataset() -> dict:
    from ai_train_core import dataset
    provider.reload_volumes([paths.DATA_DIR])
    return dataset.validate_dataset()


#-----------------------------------------------------------------------------------------
# TRAINING: one function per model size (different GPU and image), plus a router.

@provider.function(FunctionSpec(image=UNSLOTH_IMAGE, gpu=CONFIG.gpu_for("4b"),
                                timeout_s=CONFIG.train_timeout_s, volumes=TRAIN_VOLS, secrets=secrets))
def train_4b(num_train_steps: int = 100, dataset: str = "", job_id: str = "") -> dict:
    from ai_train_core import bootstrap
    return bootstrap.run_train(num_train_steps, "4b", commit_fn=provider.commit_volumes, dataset=dataset, job_id=job_id,
                               reload_fn=provider.reload_volumes)


@provider.function(FunctionSpec(image=UNSLOTH_IMAGE, gpu=CONFIG.gpu_for("12b"),
                                timeout_s=CONFIG.train_timeout_s, volumes=TRAIN_VOLS, secrets=secrets))
def train_12b(num_train_steps: int = 100, dataset: str = "", job_id: str = "") -> dict:
    from ai_train_core import bootstrap
    return bootstrap.run_train(num_train_steps, "12b", commit_fn=provider.commit_volumes, dataset=dataset, job_id=job_id,
                               reload_fn=provider.reload_volumes)


TRAIN_FUNCTIONS = {"4b": train_4b, "12b": train_12b}


# The router only waits on the GPU job, so its timeout must match the job's: a shorter one gets
# the wrapper cancelled mid-training (and the blocked wrapper is then force-killed, aborting the run).
@provider.function(FunctionSpec(image=CPU_IMAGE, timeout_s=CONFIG.train_timeout_s))
def train(num_train_steps: int = 100, model_size: str = "4b", dataset: str = "", job_id: str = "") -> dict:
    """Route to the per-size training function. `dataset` is a file name on the data volume ("" = the default
    dataset); `job_id` names the progress file the job reports to."""
    size = model_size.lower()
    if size not in TRAIN_FUNCTIONS:
        raise ValueError(f"Invalid model_size '{model_size}'. Must be one of {list(MODEL_SIZES)}.")
    return provider.call(TRAIN_FUNCTIONS[size], num_train_steps=num_train_steps, dataset=dataset, job_id=job_id)


#-----------------------------------------------------------------------------------------
# INFERENCE with the fine-tuned adapter (same per-size split as training)

def _generate(prompt: str, max_tokens: int, temperature: float, model_size: str, model_path: str = "") -> dict:
    from ai_train_core import inference
    return inference.generate_response(
        prompt, max_tokens, temperature, model_size,
        reload_fn=lambda: provider.reload_volumes([paths.MODELS_DIR]), model_path=model_path)


@provider.function(FunctionSpec(image=UNSLOTH_IMAGE, gpu=CONFIG.gpu_for("4b"), timeout_s=600,
                                volumes=INFER_VOLS, secrets=secrets))
def generate_4b(prompt: str, max_tokens: int = 1024, temperature: float = 0.7, model_path: str = "") -> dict:
    return _generate(prompt, max_tokens, temperature, "4b", model_path)


@provider.function(FunctionSpec(image=UNSLOTH_IMAGE, gpu=CONFIG.gpu_for("12b"), timeout_s=600,
                                volumes=INFER_VOLS, secrets=secrets))
def generate_12b(prompt: str, max_tokens: int = 1024, temperature: float = 0.7, model_path: str = "") -> dict:
    return _generate(prompt, max_tokens, temperature, "12b", model_path)


GENERATE_FUNCTIONS = {"4b": generate_4b, "12b": generate_12b}


@provider.function(FunctionSpec(image=CPU_IMAGE, timeout_s=600))
def generate(prompt: str, max_tokens: int = 1024, temperature: float = 0.7, model_size: str = "4b",
             model_path: str = "") -> dict:
    """Route to the per-size inference function. `model_path` (`trained/<job id>`) picks the trained model."""
    size = model_size.lower()
    if size not in GENERATE_FUNCTIONS:
        return {"error": f"Invalid model_size '{model_size}'. Must be one of {list(MODEL_SIZES)}."}
    return provider.call(GENERATE_FUNCTIONS[size], prompt, max_tokens, temperature, model_path)


#-----------------------------------------------------------------------------------------
# WEB API

@provider.asgi_app(FunctionSpec(image=WEB_IMAGE, timeout_s=600, volumes=WEB_VOLS))
def serve_api():
    from api.api import create_api
    return create_api(
        spawn_training_job=lambda num_train_steps, model_size, dataset, job_id: provider.spawn(
            train, num_train_steps=num_train_steps, model_size=model_size, dataset=dataset, job_id=job_id),
        generate_response=lambda prompt, max_tokens, temperature, model_size, model_path="": provider.call(
            generate, prompt, max_tokens, temperature, model_size, model_path),
        commit_volumes=provider.commit_volumes,
        reload_volumes=provider.reload_volumes,
    )


#-----------------------------------------------------------------------------------------
# CLI

def _local_dataset_path() -> str:
    return get_local_paths()["gemma4_tool_calling_exp1"]["path_str"]


def _sync_local_dataset() -> dict:
    path = _local_dataset_path()
    with open(path, "rb") as f:
        content = f.read()
    print(f"Local dataset: {path} ({len(content)} bytes)")
    return provider.call(sync_dataset_to_volume, content, os.path.getmtime(path))


@provider.local_entrypoint()
def main(action: str = "ping", train_steps: int = 5, model_size: str = "4b", dataset: str = "",
         model_path: str = "", prompt: str = "Check recent news about NVIDIA and buy 10 shares if it's positive"):
    print(f"provider={provider.name} app={CONFIG.app_name} action={action} model_size={model_size}")
    if action == "ping":
        print(provider.call(ping))
    elif action == "sync":
        if not os.path.exists(_local_dataset_path()):
            print(f"Dataset not found at {_local_dataset_path()}. Run: cd finetune_data && python generate_dataset.py")
            return
        print(_sync_local_dataset())
    elif action == "validate":
        print(provider.call(validate_dataset))
    elif action == "train":
        if os.path.exists(_local_dataset_path()):
            print("[1/2] Syncing dataset:", _sync_local_dataset())
        else:
            print("[1/2] Local dataset not found, using the existing remote dataset")
        print(f"[2/2] Fine-tuning {model_size} for {train_steps} steps...")
        print(provider.call(train, num_train_steps=train_steps, model_size=model_size, dataset=dataset))
    elif action == "generate":
        print(provider.call(generate, prompt, 1024, 0.7, model_size, model_path))
    else:
        print(f"unknown action {action!r}; valid: ping, sync, validate, train, generate")
