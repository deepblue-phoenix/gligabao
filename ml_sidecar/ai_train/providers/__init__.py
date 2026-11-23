"""Provider factory. Add new providers here."""
from providers.base import CallStatus, FunctionSpec, ImageSpec, Provider

__all__ = ["CallStatus", "FunctionSpec", "ImageSpec", "Provider", "get_provider"]


def get_provider(name: str, *, app_name: str, local_pkg_dir: str, volumes: dict) -> Provider:
    if name == "modal":
        from providers.modal.provider import ModalProvider  # lazy: SDK only needed if used
        return ModalProvider(app_name, local_pkg_dir, volumes)
    raise ValueError(f"unknown provider {name!r} (available: modal)")
