"""Training bootstrap, provider-neutral wiring.

Run (Modal):
    cd side_car/ai_train
    modal run app.py                         # ping: checks image/volumes/GPU
    modal run app.py --action train --train-steps 5

Cloud-specific code lives in providers/. This file only declares *what* to run.
"""
import os
import sys

modd_str = os.path.abspath(os.path.dirname(__file__))
# Local: this dir. In the container: the mounted code dir (see providers/modal_provider.py).
for _p in (modd_str, "/pkg"):
    if _p not in sys.path:
        sys.path.insert(0, _p)

from config import CONFIG
from providers import FunctionSpec, ImageSpec, get_provider

provider = get_provider(
    CONFIG.provider,
    app_name=CONFIG.app_name,
    local_pkg_dir=modd_str,
    volumes=CONFIG.volumes,
)
# Provider CLIs (e.g. `modal run`) look for this module-level object.
app = provider.native_app

secrets = (CONFIG.hf_secret,) if CONFIG.hf_secret else ()
VOLUME_PATHS = tuple(CONFIG.volumes)

# Light image for checks, train image for the real job. Add pip deps to requirements_train.txt.
CPU_IMAGE = ImageSpec(gpu=False)
TRAIN_IMAGE = ImageSpec(gpu=bool(CONFIG.gpu),
                        requirements_path=f"{modd_str}/requirements_train.txt")


@provider.function(FunctionSpec(image=CPU_IMAGE, timeout_s=300,
                                volumes=CONFIG.volumes, secrets=secrets))
def ping() -> dict:
    from ai_train_core import bootstrap
    return bootstrap.run_ping(list(VOLUME_PATHS))


@provider.function(FunctionSpec(image=TRAIN_IMAGE, gpu=CONFIG.gpu,
                                timeout_s=CONFIG.train_timeout_s,
                                volumes=CONFIG.volumes, secrets=secrets))
def train(num_train_steps: int = 100) -> dict:
    from ai_train_core import bootstrap
    result = bootstrap.run_train(num_train_steps)
    provider.commit_volumes()
    return result


@provider.local_entrypoint()
def main(action: str = "ping", train_steps: int = 5):
    print(f"provider={provider.name} app={CONFIG.app_name} gpu={CONFIG.gpu or 'none'} action={action}")
    if action == "ping":
        print(provider.call(ping))
    elif action == "train":
        print(provider.call(train, num_train_steps=train_steps))
    else:
        print(f"unknown action {action!r}; valid: ping, train")
