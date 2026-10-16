"""Modal backend: the web API of the `gligabao-train` Modal app (side_car/ai_train/app.py, `serve_api`)."""
import asyncio
from typing import Optional

from backends.base import BackendError
from backends.http_backend import HttpBackend


class ModalBackend(HttpBackend):
    name = "modal"

    def __init__(self, app_name: str, function_name: str = "serve_api", web_url: Optional[str] = None) -> None:
        super().__init__()
        self._app_name = app_name
        self._function_name = function_name
        self._url = web_url.rstrip("/") if web_url else None

    async def base_url(self) -> str:
        """The override if given, else the web URL of the deployed app (`modal deploy app.py`), looked up once
        through the Modal SDK with the MODAL_TOKEN_ID / MODAL_TOKEN_SECRET of the environment."""
        if self._url is None:
            try:
                self._url = (await asyncio.to_thread(self._lookup_web_url)).rstrip("/")
            except Exception as e:
                raise BackendError(
                    f"cannot find the web URL of Modal app {self._app_name!r} (is it deployed? "
                    f"`modal deploy app.py`, or set SIDECAR_MODAL_WEB_URL): {e}") from e
        return self._url

    def _lookup_web_url(self) -> str:
        import modal
        url = modal.Function.from_name(self._app_name, self._function_name).get_web_url()
        if not url:
            raise RuntimeError("function has no web URL")
        return url
