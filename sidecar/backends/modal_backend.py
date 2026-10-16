"""Modal backend: the web API of the `gligabao-train` Modal app (sidecar/ai_train/app.py, `serve_api`)."""
import asyncio
import json
import os
import sys
from pathlib import Path
from typing import Dict, Optional

from backends.actions import ActionRunner, Stage
from backends.base import BackendError
from backends.http_backend import HttpBackend

DEFAULT_AI_TRAIN_DIR = Path(__file__).resolve().parent.parent / "ai_train"   # the code that gets deployed
_VOLUMES_TIMEOUT = 60.0   # seconds for list_volumes.py
_STATUS_TIMEOUT = 10.0   # seconds for the Modal lookup of status()


class ModalBackend(HttpBackend):
    name = "modal"

    def __init__(self, app_name: str, function_name: str = "serve_api", web_url: Optional[str] = None,
                 token_id: Optional[str] = None, token_secret: Optional[str] = None,
                 ai_train_dir: Optional[Path] = None) -> None:
        super().__init__()
        self._app_name = app_name
        self._function_name = function_name
        self._url = web_url.rstrip("/") if web_url else None
        self._ai_train_dir = ai_train_dir or DEFAULT_AI_TRAIN_DIR
        # The helper scripts run in a subprocess (their own process: no `modal` import side effects here, clean log
        # capture, and no `modal` CLI and no ~/.modal.toml needed: the credentials come in the environment).
        self._script_env = {"SIDECAR_APP_NAME": app_name, "PYTHONUNBUFFERED": "1", "NO_COLOR": "1", "TERM": "dumb",
                            "COLUMNS": "200"}
        if token_id:
            self._script_env["MODAL_TOKEN_ID"] = token_id
        if token_secret:
            self._script_env["MODAL_TOKEN_SECRET"] = token_secret
        deploy, stop = self._make_deploy(), self._make_stop()
        deploy.exclusive_with, stop.exclusive_with = [stop], [deploy]
        self._actions = {"deploy": deploy, "stop": stop}

    def _make_deploy(self) -> ActionRunner:
        """`deploy`: ai_train/deploy_modal.py (the SDK's `app.deploy()`); its output is kept for the admin UI."""
        # Heuristic stages from Modal's output; the log is the source of truth. Stages only move forward.
        stages = (Stage("starting"),
                  Stage("building images", r"building image|=> step|built image"),
                  Stage("creating functions", r"creating objects|created (objects|function|web function|class)"),
                  Stage("deployed"))
        return ActionRunner("deploy", "Deploy the Modal app", [sys.executable, "-u", "deploy_modal.py"],
                            cwd=str(self._ai_train_dir), env=self._script_env, stages=stages)

    def _make_stop(self) -> ActionRunner:
        """`stop`: ai_train/stop_modal.py (`modal app stop`): ends the app's containers (running training jobs too)."""
        return ActionRunner(
            "stop", "Stop the Modal app", [sys.executable, "-u", "stop_modal.py"], cwd=str(self._ai_train_dir),
            env=self._script_env, danger=True,
            confirm=f"Stop the Modal app {self._app_name!r}?\n\nRunning training jobs and inference on it are terminated and its "
                    "web API goes offline until you deploy it again. The volumes (datasets, adapters) are kept.")

    async def volumes(self) -> list:
        """The volumes of the app with what Modal knows about them (ai_train/list_volumes.py, read only)."""
        try:
            proc = await asyncio.create_subprocess_exec(
                sys.executable, "list_volumes.py", cwd=str(self._ai_train_dir), env={**os.environ, **self._script_env},
                stdin=asyncio.subprocess.DEVNULL, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
            try:
                out, err = await asyncio.wait_for(proc.communicate(), _VOLUMES_TIMEOUT)
            except asyncio.TimeoutError:
                proc.kill()
                await proc.wait()
                raise BackendError(f"listing the Modal volumes timed out after {_VOLUMES_TIMEOUT:.0f}s")
            if proc.returncode != 0:
                tail = err.decode("utf-8", "replace").strip().splitlines()[-1:] or [f"exit code {proc.returncode}"]
                raise BackendError(f"listing the Modal volumes failed: {tail[0]}")
            return json.loads(out)["volumes"]
        except BackendError:
            raise
        except Exception as e:
            raise BackendError(f"listing the Modal volumes failed: {e}") from e

    def actions(self) -> Dict[str, ActionRunner]:
        return self._actions

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
        st["actions"] = self.actions_status(skip=("stop",) if st["deployed"] is False else ())   # nothing to stop
        return st

    def _lookup_web_url(self) -> str:
        import modal
        url = modal.Function.from_name(self._app_name, self._function_name).get_web_url()
        if not url:
            raise RuntimeError("function has no web URL")
        return url
