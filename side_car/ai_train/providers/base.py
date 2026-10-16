"""Provider interface. All cloud-specific code lives behind this.

To add a provider (e.g. AWS): implement `Provider` in `providers/<name>_provider.py`
and register it in `providers/__init__.py`. Nothing outside `providers/` may import
a provider SDK.
"""
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Any, Callable, Optional


@dataclass(frozen=True)
class ImageSpec:
    """Container image description, provider neutral."""
    gpu: bool = False                 # needs a CUDA base image
    requirements_path: str = ""       # pip requirements file ("" or no entries = nothing to install)
    python: str = "3.11"


@dataclass(frozen=True)
class FunctionSpec:
    """How a remote function should run."""
    image: ImageSpec
    gpu: str = ""                                       # "" = CPU only
    timeout_s: int = 600
    volumes: dict = field(default_factory=dict)         # mount path -> volume name
    secrets: tuple = ()                                 # secret names


class Provider(ABC):
    name: str

    @abstractmethod
    def function(self, spec: FunctionSpec) -> Callable:
        """Decorator: register `fn` as a remote function. Returns a handle for call()/spawn()."""

    @abstractmethod
    def asgi_app(self, spec: FunctionSpec) -> Callable:
        """Decorator: register a factory returning an ASGI app (e.g. FastAPI) as a served web endpoint."""

    @abstractmethod
    def local_entrypoint(self) -> Callable:
        """Decorator: register a local CLI entrypoint that orchestrates remote calls."""

    @abstractmethod
    def call(self, handle: Any, *args, **kwargs) -> Any:
        """Run a remote function and block for its result."""

    @abstractmethod
    def spawn(self, handle: Any, *args, **kwargs) -> str:
        """Start a remote function without waiting. Returns a job id."""

    @abstractmethod
    def commit_volumes(self, paths: Optional[list] = None) -> None:
        """Persist writes made to volumes inside a running remote function.

        `paths` = mount paths to commit; None = all volumes.
        """

    @abstractmethod
    def reload_volumes(self, paths: Optional[list] = None) -> None:
        """Refresh volumes to see writes committed by other containers. `paths` as in commit_volumes."""

    @property
    @abstractmethod
    def native_app(self) -> Any:
        """The provider's own app object, for provider CLIs that need it (e.g. `modal run`)."""
