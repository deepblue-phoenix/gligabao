"""A backend that is a remote HTTP service implementing the sidecar API: requests are forwarded as they are."""
from abc import abstractmethod
from typing import Optional

import httpx

from backends.base import Backend, BackendError, BackendResponse

# job_events is an SSE stream that stays open for minutes (keepalive comments while idle): no read timeout.
_TIMEOUT = httpx.Timeout(connect=15.0, read=None, write=60.0, pool=15.0)


class HttpBackend(Backend):
    def __init__(self) -> None:
        self._client = httpx.AsyncClient(timeout=_TIMEOUT)

    @abstractmethod
    async def base_url(self) -> str:
        """The URL the API is served at (no trailing slash). Raises BackendError if it cannot be determined."""

    async def forward(self, method: str, path: str, query: str, body: bytes,
                      content_type: Optional[str]) -> BackendResponse:
        url = await self.base_url() + path + (f"?{query}" if query else "")
        headers = {"content-type": content_type} if content_type else {}
        try:
            resp = await self._client.send(
                self._client.build_request(method, url, content=body or None, headers=headers), stream=True)
        except httpx.HTTPError as e:
            raise BackendError(f"{self.name} backend unreachable: {e!r}") from e
        return BackendResponse(status=resp.status_code, content_type=resp.headers.get("content-type"),
                               body=resp.aiter_raw(), close=resp.aclose)

    async def aclose(self) -> None:
        await self._client.aclose()
