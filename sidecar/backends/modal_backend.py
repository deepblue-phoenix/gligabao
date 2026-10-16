"""Modal backend: the web API of the `gligabao-train` Modal app (sidecar/ai_train/app.py, `serve_api`)."""
import asyncio
from typing import Optional

from backends.base import BackendError
from backends.http_backend import HttpBackend

_STATUS_TIMEOUT = 10.0   # seconds for the Modal lookup of status()


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

    async def status(self) -> dict:
        """Healthy = Modal answers with these credentials. `deployed` = the app's web function exists in Modal
        (False: run `modal deploy app.py`; None: could not be checked)."""
        st = {"name": self.name, "configured": True, "healthy": False, "deployed": None, "app": self._app_name}
        try:
            await asyncio.wait_for(asyncio.to_thread(self._lookup_web_url), timeout=_STATUS_TIMEOUT)
            st.update(healthy=True, deployed=True)
        except Exception as e:
            import modal.exception as mexc
            if isinstance(e, mexc.NotFoundError):      # Modal answered: it just has no such app / function
                st.update(healthy=True, deployed=False)
            else:
                st["error"] = str(e) or type(e).__name__
        return st

    def _lookup_web_url(self) -> str:
        import modal
        url = modal.Function.from_name(self._app_name, self._function_name).get_web_url()
        if not url:
            raise RuntimeError("function has no web URL")
        return url
