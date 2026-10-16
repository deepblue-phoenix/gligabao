"""Backends of the sidecar API. `get_backend(config)` builds the one the config selects."""
from backends.base import Backend, BackendError, BackendResponse

__all__ = ["Backend", "BackendError", "BackendResponse", "get_backend"]


def get_backend(config) -> Backend:
    if config.backend == "modal":
        from backends.modal_backend import ModalBackend
        secret = lambda v: v.get_secret_value() if v else None
        return ModalBackend(config.modal_app_name, web_url=config.modal_web_url,
                            token_id=secret(config.modal_token_id), token_secret=secret(config.modal_token_secret))
    raise ValueError(f"unknown sidecar backend {config.backend!r}")
