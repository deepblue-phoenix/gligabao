"""Modal backend: the web API of the `gligabao-train` Modal app (sidecar/ai_train/app.py, `serve_api`)."""
import asyncio
import json
import os
import re
import sys
from pathlib import Path
from typing import Dict, Optional

from backends.actions import ActionRunner, Stage
from backends.base import BackendError
from backends.http_backend import HttpBackend

DEFAULT_AI_TRAIN_DIR = Path(__file__).resolve().parent.parent / "ai_train"   # the code that gets deployed
_VOLUMES_TIMEOUT = 60.0   # seconds for providers/modal/list_volumes.py
_STATUS_TIMEOUT = 10.0   # seconds for the Modal lookup of status()
_APPS_TIMEOUT = 30.0   # seconds for providers/modal/list_apps.py
_ACTIVE_ONLY = ("stop", "stop_inference")   # actions that make no sense while the app is not deployed
_APP_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")   # what a Modal app name may look like


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
        self._app_actions: Dict[str, Dict[str, ActionRunner]] = {}   # per app name, created on first use
        self._actions = self.app_actions(app_name)

    def owns_app(self, app: str) -> bool:
        """Whether `app` belongs to this system: the configured app (SIDECAR_APP_NAME) or one named `<it>-<suffix>`
        (e.g. a dev copy). Other apps of the Modal workspace are not ours to deploy or stop."""
        return bool(_APP_NAME.match(app)) and (app == self._app_name or app.startswith(self._app_name + "-"))

    def app_actions(self, app: str) -> Dict[str, ActionRunner]:
        """The deploy / stop actions of one app of this system ({} if it is not one)."""
        if not self.owns_app(app):
            return {}
        if app not in self._app_actions:
            deploy, stop = self._make_deploy(app), self._make_stop(app)
            deploy.exclusive_with, stop.exclusive_with = [stop], [deploy]
            self._app_actions[app] = {"deploy": deploy, "stop": stop, "stop_inference": self._make_stop_inference(app)}
        return self._app_actions[app]

    def _env_for(self, app: str) -> dict:
        return {**self._script_env, "SIDECAR_APP_NAME": app}

    def _make_deploy(self, app: str) -> ActionRunner:
        """`deploy`: ai_train/providers/modal/deploy.py (the SDK's `app.deploy()`); its output is kept for the admin UI."""
        # Heuristic stages from Modal's output; the log is the source of truth. Stages only move forward.
        stages = (Stage("starting"),
                  Stage("building images", r"building image|=> step|built image"),
                  Stage("creating functions", r"creating objects|created (objects|function|web function|class)"),
                  Stage("deployed"))
        return ActionRunner("deploy", "Deploy the Modal app", [sys.executable, "-u", "-m", "providers.modal.deploy"],
                            cwd=str(self._ai_train_dir), env=self._env_for(app), stages=stages)

    def _make_stop(self, app: str) -> ActionRunner:
        """`stop`: ai_train/providers/modal/stop.py (`modal app stop`): ends the app's containers (running training jobs too)."""
        return ActionRunner(
            "stop", "Stop the Modal app", [sys.executable, "-u", "-m", "providers.modal.stop"], cwd=str(self._ai_train_dir),
            env=self._env_for(app), danger=True,
            confirm=f"Stop the Modal app {app!r}?\n\nRunning training jobs and inference on it are terminated and its "
                    "web API goes offline until you deploy it again. The volumes (datasets, adapters) are kept.")

    def _make_stop_inference(self, app: str) -> ActionRunner:
        """`stop_inference`: ai_train/providers/modal/inference_containers.py: ends the warm inference containers (the model loaded in
        each is dropped) and nothing else; the next inference request starts one again."""
        return ActionRunner(
            "stop_inference", "Stop the inference containers", [sys.executable, "-u", "-m", "providers.modal.inference_containers"],
            cwd=str(self._ai_train_dir), env=self._env_for(app), danger=True,
            confirm=f"Stop the inference containers of {app!r}?\n\nThe models loaded in them are dropped and a request "
                    "being answered now fails. The app, its web API and training jobs are not touched; the next "
                    "inference request starts a container again (and loads the model, which takes a while).")

    async def apps(self) -> list:
        """The active Modal apps of this system (ai_train/providers/modal/list_apps.py, read only), each with its actions' state. The
        configured app is always listed (not active: `active` false, so it can be deployed), and so is any app of ours
        with an action that ran since the sidecar started."""
        found = (await self._run_json("providers.modal.list_apps", _APPS_TIMEOUT, "listing the Modal apps"))["apps"]
        by_name = {a["name"]: {**a, "active": True} for a in found if self.owns_app(a["name"])}
        for name in dict.fromkeys([self._app_name, *self._app_actions]):
            ran = any(a.state()["state"] != "idle" for a in self._app_actions[name].values())
            if name not in by_name and (ran or name == self._app_name):
                by_name[name] = {"name": name, "app_id": None, "state": "not deployed", "n_running_tasks": 0,
                                 "created_at": None, "inference": [], "active": False}
        out = []
        for name in sorted(by_name, key=lambda n: (n != self._app_name, n)):
            actions = self.app_actions(name)
            active = by_name[name]["active"]
            out.append({**by_name[name], "configured": name == self._app_name,
                        "actions": {k: a.state() for k, a in actions.items() if active or k not in _ACTIVE_ONLY}})
        return out

    async def _run_json(self, module: str, timeout: float, what: str) -> dict:
        """Run a read-only helper module (`python -m`) that prints JSON; BackendError if it fails or is too slow."""
        try:
            proc = await asyncio.create_subprocess_exec(
                sys.executable, "-m", module, cwd=str(self._ai_train_dir), env={**os.environ, **self._script_env},
                stdin=asyncio.subprocess.DEVNULL, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
            try:
                out, err = await asyncio.wait_for(proc.communicate(), timeout)
            except asyncio.TimeoutError:
                proc.kill()
                await proc.wait()
                raise BackendError(f"{what} timed out after {timeout:.0f}s")
            if proc.returncode != 0:
                tail = err.decode("utf-8", "replace").strip().splitlines()[-1:] or [f"exit code {proc.returncode}"]
                raise BackendError(f"{what} failed: {tail[0]}")
            return json.loads(out)
        except BackendError:
            raise
        except Exception as e:
            raise BackendError(f"{what} failed: {e}") from e

    async def volumes(self) -> list:
        """The volumes of the app with what Modal knows about them (ai_train/providers/modal/list_volumes.py, read only)."""
        return (await self._run_json("providers.modal.list_volumes", _VOLUMES_TIMEOUT, "listing the Modal volumes"))["volumes"]

    def actions(self) -> Dict[str, ActionRunner]:
        return self._actions

    async def aclose(self) -> None:
        for actions in list(self._app_actions.values()):
            for a in actions.values():
                await a.aclose()

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
        st["actions"] = self.actions_status(skip=_ACTIVE_ONLY if st["deployed"] is False else ())   # nothing to stop
        return st

    def _lookup_web_url(self) -> str:
        import modal
        url = modal.Function.from_name(self._app_name, self._function_name).get_web_url()
        if not url:
            raise RuntimeError("function has no web URL")
        return url
