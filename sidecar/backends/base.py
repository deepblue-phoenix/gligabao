"""The sidecar's backend interface.

The sidecar's REST API (the routes in `routes.py`) is the only contract the Go server knows. What stands behind it
(Modal today, anything else later) is a `Backend`: it receives the already-validated request and answers it.
Adding a backend means adding a class here that implements `forward` and registering it in `backends/__init__.py`.
"""
from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import AsyncIterator, Awaitable, Callable, Dict, Optional

from backends.actions import ActionRunner


@dataclass
class BackendResponse:
    """A backend's answer. `body` may be a long-lived stream (SSE), so it is iterated, not read at once;
    `close` releases the underlying connection and must be called once the body is consumed or abandoned."""
    status: int
    content_type: Optional[str]
    body: AsyncIterator[bytes]
    close: Callable[[], Awaitable[None]]


class BackendError(Exception):
    """The backend could not be reached or did not answer (the sidecar turns it into a 502)."""


class Backend(ABC):
    name: str

    @abstractmethod
    async def forward(self, method: str, path: str, query: str, body: bytes,
                      content_type: Optional[str]) -> BackendResponse:
        """Perform `method path?query` with `body` against the backend. Raises BackendError if it is unreachable;
        an error answer from the backend itself (4xx/5xx) is returned as a normal BackendResponse."""

    def actions(self) -> Dict[str, ActionRunner]:
        """The long-running actions an operator can start for this backend, by name (e.g. "deploy"); see
        backends/actions.py. They are exposed under `/backends/<name>/actions/<action>` and listed in `status()`."""
        return {}

    def actions_status(self, skip: tuple = ()) -> dict:
        """The state of the actions (without those named in `skip`, e.g. ones that make no sense right now)."""
        return {k: a.state() for k, a in self.actions().items() if k not in skip}

    def app_actions(self, app: str) -> Dict[str, ActionRunner]:
        """The actions of one app of the backend, by name (`/backends/<name>/apps/<app>/actions/<action>`); {} if the
        backend has no such app."""
        return {}

    async def apps(self) -> Optional[list]:
        """The active apps of this system on the backend, one dict each (`GET /backends/<name>/apps`; at least `name`
        and `actions`: the state of `app_actions(name)`), or None if the backend has no such concept. Raises
        BackendError if they cannot be listed."""
        return None

    async def volumes(self) -> Optional[list]:
        """The storage volumes the backend uses, one dict each (`GET /backends/<name>/volumes`), or None if the backend
        has no such concept. Raises BackendError if they cannot be listed."""
        return None

    async def aclose(self) -> None:
        """Release backend resources at shutdown (and stop running actions)."""
        for a in self.actions().values():
            await a.aclose()

    async def status(self) -> dict:
        """What `GET /backends` reports about this backend: `name`, `configured` and `healthy` (it can be used), plus
        backend-specific fields (`error` explains an unhealthy one). Must not raise and should be quick."""
        return {"name": self.name, "configured": True, "healthy": True, "actions": self.actions_status()}
