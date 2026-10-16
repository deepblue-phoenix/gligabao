"""Modal implementation of Provider. The ONLY file that imports `modal`."""
import os
from typing import Any, Callable

import modal

from providers.base import FunctionSpec, ImageSpec, Provider

# Where the sidecar's ai_train dir is mounted inside the container.
REMOTE_PKG_DIR = "/pkg"


def _has_requirements(path: str) -> bool:
    if not path or not os.path.exists(path):
        return False
    with open(path) as f:
        return any(l.strip() and not l.strip().startswith(("#", "--")) for l in f)


class ModalProvider(Provider):
    name = "modal"

    def __init__(self, app_name: str, local_pkg_dir: str, volume_names: dict):
        self._app = modal.App(app_name)
        self._local_pkg_dir = local_pkg_dir
        self._volumes = {
            path: modal.Volume.from_name(name, create_if_missing=True)
            for path, name in volume_names.items()
        }
        self._images: dict = {}

    @property
    def native_app(self) -> Any:
        return self._app

    # ---- images ----
    def _image(self, spec: ImageSpec) -> "modal.Image":
        if spec in self._images:
            return self._images[spec]
        if spec.gpu:
            img = modal.Image.from_registry(
                "nvidia/cuda:12.4.1-devel-ubuntu22.04", add_python=spec.python)
        else:
            img = modal.Image.debian_slim(python_version=spec.python)
        if _has_requirements(spec.requirements_path):
            img = img.pip_install_from_requirements(spec.requirements_path)
        # Last step: mount our code (no rebuild on code changes).
        img = img.add_local_dir(
            self._local_pkg_dir, remote_path=REMOTE_PKG_DIR,
            ignore=["__pycache__", "*.pyc"])
        self._images[spec] = img
        return img

    # ---- functions ----
    def function(self, spec: FunctionSpec) -> Callable:
        kwargs = dict(
            image=self._image(spec.image),
            timeout=spec.timeout_s,
            volumes={p: self._volumes[p] for p in spec.volumes},
            secrets=[modal.Secret.from_name(s) for s in spec.secrets],
        )
        if spec.gpu:
            kwargs["gpu"] = spec.gpu
        return self._app.function(**kwargs)

    def local_entrypoint(self) -> Callable:
        return self._app.local_entrypoint()

    def call(self, handle: Any, *args, **kwargs) -> Any:
        return handle.remote(*args, **kwargs)

    def spawn(self, handle: Any, *args, **kwargs) -> str:
        return handle.spawn(*args, **kwargs).object_id

    def commit_volumes(self) -> None:
        for v in self._volumes.values():
            v.commit()
