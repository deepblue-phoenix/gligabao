"""The sidecar's backend interface.

The sidecar's REST API (the routes in `routes.py`) is the only contract the Go server knows. What stands behind it
(Modal today, anything else later) is a `Backend`: it receives the already-validated request and answers it.
Adding a backend means adding a class here that implements `forward` and registering it in `backends/__init__.py`.
"""
from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import AsyncIterator, Awaitable, Callable, Optional


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

    async def aclose(self) -> None:
        """Release backend resources at shutdown."""
