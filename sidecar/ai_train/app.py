"""Training bootstrap, provider-neutral wiring.

Run (Modal):
    cd sidecar/ai_train
    modal run app.py                                          # ping: checks image/volumes
    modal run app.py --action datasets                        # list the local datasets (build/datasets_training/<name>/dataset.toml)
    modal run app.py --action sync --dataset <name>           # upload a local dataset to the data volume (if it changed)
    modal run app.py --action validate --dataset <name>       # validate the dataset on the data volume
    modal run app.py --action train --dataset <name> --train-steps 100 --model-size 4b   # 4b (A10G), 12b (A100) or 31b (A100 80GB);
                                                              # uploads the (local) dataset first
    modal run app.py --action infer --model-size 4b --prompt "..."      # fine-tuned model inference
                                                              # [--model-path trained/<job id>: a trained model; default: the legacy adapter]
    modal serve app.py                                        # web API (dev); `modal deploy app.py` to persist

Cloud-specific code lives in providers/<name>/ (providers/modal/). This file only declares *what* to run.

Images (see below): training uses Unsloth on a CUDA toolkit image (requirements_train.txt); inference uses a separate,
lighter one without Unsloth (requirements_infer.txt); the CPU functions and the web API have none of the ML stack.
"""
import os
import sys

modd_str = os.path.abspath(os.path.dirname(__file__))
# Local: this dir. In the container: the mounted code dir (see providers/modal/provider.py).
for _p in (modd_str, "/pkg"):
    if _p not in sys.path:
        sys.path.insert(0, _p)

from ai_train_core import paths
from ai_train_core.model_families import model_sizes

MODEL_SIZES = model_sizes()   # of the default model family
from config import CONFIG
from providers import FunctionSpec, ImageSpec, get_provider
from training_data import DatasetError, find_local_dataset, list_local_datasets

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
TRAIN_VOLS = _volumes(paths.DATA_DIR, paths.MODELS_DIR, paths.HF_CACHE_DIR)
INFER_VOLS = _volumes(paths.MODELS_DIR, paths.HF_CACHE_DIR)
WEB_VOLS = _volumes(paths.DATA_DIR, paths.EVAL_DIR)

# Images. Code is mounted last, so code edits don't rebuild the pip layers.
CPU_IMAGE = ImageSpec(gpu=False)
WEB_IMAGE = ImageSpec(gpu=False, requirements_path=f"{modd_str}/requirements_web.txt")
UNSLOTH_IMAGE = ImageSpec(gpu=True, requirements_path=f"{modd_str}/requirements_train.txt")             # training, 4B, 12B and 31B
# Inference only: transformers + peft + bitsandbytes on a slim base (no Unsloth, no trainer, no CUDA toolkit). 4B, 12B and 31B.
INFER_IMAGE = ImageSpec(gpu=True, cuda_toolkit=False, requirements_path=f"{modd_str}/requirements_infer.txt")


#-----------------------------------------------------------------------------------------
# CHECKS / DATASET

@provider.function(FunctionSpec(
    image=CPU_IMAGE,
    timeout_s=300,
    volumes=ALL_VOLUMES,
    secrets=secrets))
def ping() -> dict:
    from ai_train_core import bootstrap
    return bootstrap.run_ping(list(VOLUME_PATHS))


@provider.function(FunctionSpec(image=CPU_IMAGE, timeout_s=300, volumes=DATA_VOLS))
def sync_dataset_to_volume(dataset_content: bytes, filename: str) -> dict:
    """Write the dataset file `filename` (<dataset name>.jsonl) to the data volume unless it is there as it is."""
    from ai_train_core import dataset
    return dataset.sync_dataset(dataset_content, filename, commit_fn=provider.commit_volumes)


@provider.function(FunctionSpec(image=CPU_IMAGE, timeout_s=300, volumes=DATA_VOLS))
def validate_dataset(filename: str) -> dict:
    from ai_train_core import dataset
    provider.reload_volumes([paths.DATA_DIR])
    return dataset.validate_dataset(dataset.resolve_dataset_path(filename))


#-----------------------------------------------------------------------------------------
# TRAINING: one function per model size (different GPU and image), plus a router.

@provider.function(FunctionSpec(
    image=UNSLOTH_IMAGE,
    gpu=CONFIG.gpu_for("4b"),
    timeout_s=CONFIG.train_timeout_s,
    volumes=TRAIN_VOLS,
    secrets=secrets))
def train_4b(dataset: str, num_train_steps: int = 100, job_id: str = "") -> dict:
    from ai_train_core import bootstrap
    return bootstrap.run_train(num_train_steps, "4b", commit_fn=provider.commit_volumes, dataset=dataset, job_id=job_id,
                               event_kv=provider.event_kv())


@provider.function(FunctionSpec(
    image=UNSLOTH_IMAGE,
    gpu=CONFIG.gpu_for("12b"),
    timeout_s=CONFIG.train_timeout_s,
    volumes=TRAIN_VOLS,
    secrets=secrets))
def train_12b(dataset: str, num_train_steps: int = 100, job_id: str = "") -> dict:
    from ai_train_core import bootstrap
    return bootstrap.run_train(num_train_steps, "12b", commit_fn=provider.commit_volumes, dataset=dataset, job_id=job_id,
                               event_kv=provider.event_kv())


@provider.function(FunctionSpec(
    image=UNSLOTH_IMAGE,
    gpu=CONFIG.gpu_for("31b"),
    timeout_s=CONFIG.train_timeout_s,
    volumes=TRAIN_VOLS,
    secrets=secrets))
def train_31b(dataset: str, num_train_steps: int = 100, job_id: str = "") -> dict:
    from ai_train_core import bootstrap
    return bootstrap.run_train(num_train_steps, "31b", commit_fn=provider.commit_volumes, dataset=dataset, job_id=job_id,
                               event_kv=provider.event_kv())


TRAIN_FUNCTIONS = {"4b": train_4b, "12b": train_12b, "31b": train_31b}


# The router only waits on the GPU job, so its timeout must match the job's: a shorter one gets
# the wrapper cancelled mid-training (and the blocked wrapper is then force-killed, aborting the run).
@provider.function(FunctionSpec(image=CPU_IMAGE, timeout_s=CONFIG.train_timeout_s))
def train(dataset: str, num_train_steps: int = 100, model_size: str = "4b", job_id: str = "") -> dict:
    """Route to the per-size training function. `dataset` is a file name on the data volume (<dataset name>.jsonl);
    `job_id` names the event log the job reports to."""
    size = model_size.lower()
    if size not in TRAIN_FUNCTIONS:
        raise ValueError(f"Invalid model_size '{model_size}'. Must be one of {list(MODEL_SIZES)}.")
    return provider.call(TRAIN_FUNCTIONS[size], num_train_steps=num_train_steps, dataset=dataset, job_id=job_id)


#-----------------------------------------------------------------------------------------
# INFERENCE with the fine-tuned adapter (same per-size split as training)

# An inference container keeps its model loaded and stays up for CONFIG.infer_idle_timeout_s after its last request, so
# later requests skip the (slow) model load. It records itself with the provider (provider.register_container) so the
# admin UI can list it and stop it before the timeout (sidecar/ai_train/providers/modal/inference_containers.py).
_registered = False


def _infer(function: str, prompt: str, max_tokens: int, temperature: float, model_size: str, model_path: str = "") -> dict:
    from ai_train_core import inference

    def on_ready(info: dict) -> None:
        global _registered
        provider.register_container({"function": function, **info})
        if not _registered:
            import atexit
            atexit.register(provider.unregister_container)
            _registered = True

    return inference.infer_response(
        prompt, max_tokens, temperature, model_size,
        reload_fn=lambda: provider.reload_volumes([paths.MODELS_DIR]), model_path=model_path, on_ready=on_ready)


@provider.function(FunctionSpec(
    image=INFER_IMAGE,
    gpu=CONFIG.gpu_for("4b"),
    timeout_s=600,
    idle_timeout_s=CONFIG.infer_idle_timeout_s,
    volumes=INFER_VOLS,
    secrets=secrets))
def infer_4b(prompt: str, max_tokens: int = 1024, temperature: float = 0.7, model_path: str = "") -> dict:
    return _infer("infer_4b", prompt, max_tokens, temperature, "4b", model_path)


@provider.function(FunctionSpec(
    image=INFER_IMAGE,
    gpu=CONFIG.gpu_for("12b"),
    timeout_s=600,
    idle_timeout_s=CONFIG.infer_idle_timeout_s,
    volumes=INFER_VOLS,
    secrets=secrets))
def infer_12b(prompt: str, max_tokens: int = 1024, temperature: float = 0.7, model_path: str = "") -> dict:
    return _infer("infer_12b", prompt, max_tokens, temperature, "12b", model_path)


@provider.function(FunctionSpec(
    image=INFER_IMAGE,
    gpu=CONFIG.gpu_for("31b"),
    timeout_s=600,
    idle_timeout_s=CONFIG.infer_idle_timeout_s,
    volumes=INFER_VOLS,
    secrets=secrets))
def infer_31b(prompt: str, max_tokens: int = 1024, temperature: float = 0.7, model_path: str = "") -> dict:
    return _infer("infer_31b", prompt, max_tokens, temperature, "31b", model_path)


INFER_FUNCTIONS = {"4b": infer_4b, "12b": infer_12b, "31b": infer_31b}


@provider.function(FunctionSpec(image=CPU_IMAGE, timeout_s=600))
def infer(prompt: str,
    max_tokens: int = 1024,
    temperature: float = 0.7,
    model_size: str = "4b",
    model_path: str = "") -> dict:
    """Route to the per-size inference function. `model_path` (`trained/<job id>`) picks the trained model."""
    size = model_size.lower()
    if size not in INFER_FUNCTIONS:
        return {"error": f"Invalid model_size '{model_size}'. Must be one of {list(MODEL_SIZES)}."}
    return provider.call(INFER_FUNCTIONS[size], prompt, max_tokens, temperature, model_path)


#-----------------------------------------------------------------------------------------
# WEB API

@provider.asgi_app(FunctionSpec(image=WEB_IMAGE, timeout_s=600, volumes=WEB_VOLS))
def serve_api():
    from api.api import create_api
    return create_api(
        spawn_training_job=lambda num_train_steps, model_size, dataset, job_id: provider.spawn(
            train, num_train_steps=num_train_steps, model_size=model_size, dataset=dataset, job_id=job_id),
        infer_response=lambda prompt, max_tokens, temperature, model_size, model_path="": provider.call(
            infer, prompt, max_tokens, temperature, model_size, model_path),
        commit_volumes=provider.commit_volumes,
        reload_volumes=provider.reload_volumes,
        event_kv=provider.event_kv(),
        call_status=provider.call_status,
    )


#-----------------------------------------------------------------------------------------
# CLI

def _sync_local_dataset(name: str) -> dict:
    """Upload the local dataset `name` (its manifest's data file) to the data volume, as <name>.jsonl."""
    ds = find_local_dataset(name)
    content = ds.path.read_bytes()
    print(f"Local dataset {ds.name}: {ds.path} ({len(content)} bytes)")
    return provider.call(sync_dataset_to_volume, content, ds.filename)


@provider.local_entrypoint()
def main(action: str = "ping", train_steps: int = 5, model_size: str = "4b", dataset: str = "",
         model_path: str = "", prompt: str = "Check recent news about NVIDIA and buy 10 shares if it's positive"):
    print(f"provider={provider.name} app={CONFIG.app_name} action={action} model_size={model_size}")
    try:
        _run_action(action, train_steps, model_size, dataset, model_path, prompt)
    except DatasetError as e:
        print(f"error: {e}")


def _run_action(action, train_steps, model_size, dataset, model_path, prompt) -> None:
    if action == "ping":
        print(provider.call(ping))
    elif action == "datasets":
        found, problems = list_local_datasets()
        for ds in found:
            print(f"{ds.name:30} {ds.path}  {ds.description[:60]}")
        for p in problems:
            print(f"{p}")
        if not found and not problems:
            print("no datasets: add <name>/dataset.toml directories to the training datasets directory")
    elif action == "sync":
        print(_sync_local_dataset(_need_dataset(dataset)))
    elif action == "validate":
        print(provider.call(validate_dataset, find_local_dataset(_need_dataset(dataset)).filename))
    elif action == "train":
        ds = find_local_dataset(_need_dataset(dataset))
        print("[1/2] Syncing dataset:", _sync_local_dataset(ds.name))
        print(f"[2/2] Fine-tuning {model_size} for {train_steps} steps...")
        print(provider.call(train, dataset=ds.filename, num_train_steps=train_steps, model_size=model_size))
    elif action == "infer":
        print(provider.call(infer, prompt, 1024, 0.7, model_size, model_path))
    else:
        print(f"unknown action {action!r}; valid: ping, datasets, sync, validate, train, infer")


def _need_dataset(name: str) -> str:
    if not name:
        raise DatasetError("--dataset <name> is required (see --action datasets)")
    return name
