"""A backend that is a remote HTTP service implementing the sidecar API: requests are forwarded as they are."""
from abc import abstractmethod
from typing import Optional

import logging
import time

import httpx

from backends.base import Backend, BackendError, BackendResponse

# job_events is an SSE stream that stays open for minutes (keepalive comments while idle): no read timeout.
log = logging.getLogger("uvicorn.error")   # shows next to uvicorn's access lines

# Modal answers a web request that runs longer than ~150 s with `303 See Other` + a Location to poll for the result (the
# client must follow it with GET, repeatedly, for up to 20 minutes). A cold inference takes that long, so redirects are
# followed; the limit leaves room for many polls.
_MAX_REDIRECTS = 50
_TIMEOUT = httpx.Timeout(connect=15.0, read=None, write=60.0, pool=15.0)


class HttpBackend(Backend):
    def __init__(self) -> None:
        self._client = httpx.AsyncClient(timeout=_TIMEOUT, follow_redirects=True, max_redirects=_MAX_REDIRECTS)

    @abstractmethod
    async def base_url(self) -> str:
        """The URL the API is served at (no trailing slash). Raises BackendError if it cannot be determined."""

    async def forward(self, method: str, path: str, query: str, body: bytes,
                      content_type: Optional[str]) -> BackendResponse:
        url = await self.base_url() + path + (f"?{query}" if query else "")
        # No compression: the body is relayed to the Go server, which gets no content-encoding header. (Modal's gateway
        # answers zstd when httpx offers it, which the Go client then fails to decode as JSON: "invalid character '('".)
        headers = {"accept-encoding": "identity"}
        if content_type:
            headers["content-type"] = content_type
        log.info("%s backend: -> %s %s (%d bytes)", self.name, method, url, len(body))
        started = time.monotonic()
        try:
            resp = await self._client.send(
                self._client.build_request(method, url, content=body or None, headers=headers), stream=True)
        except httpx.TooManyRedirects as e:
            log.error("%s backend: %s %s: too many redirects after %.1fs: %r", self.name, method, url,
                      time.monotonic() - started, e)
            raise BackendError(f"{self.name} backend: too many redirects: {e!r}") from e
        except httpx.HTTPError as e:
            log.error("%s backend: %s %s failed after %.1fs: %r", self.name, method, url,
                      time.monotonic() - started, e)
            raise BackendError(f"{self.name} backend unreachable: {e!r}") from e
        elapsed = time.monotonic() - started
        for r in resp.history:   # the 303s of a long request
            log.info("%s backend: %s %s redirected: %d -> %s", self.name, method, path, r.status_code,
                     r.headers.get("location"))
        ctype = resp.headers.get("content-type")
        if resp.status_code >= 400 or resp.history:
            log.log(logging.WARNING if resp.status_code >= 400 else logging.INFO,
                    "%s backend: %s %s -> %d (%s) after %.1fs, %d redirect(s)", self.name, method, path,
                    resp.status_code, ctype, elapsed, len(resp.history))
        else:
            log.info("%s backend: %s %s -> %d after %.1fs", self.name, method, path, resp.status_code, elapsed)
        if resp.status_code >= 400 and "text/event-stream" not in (ctype or ""):
            # An error answer is small: read it to log it, then relay it.
            try:
                data = await resp.aread()
            except httpx.HTTPError as e:
                await resp.aclose()
                raise BackendError(f"{self.name} backend: reading the error answer failed: {e!r}") from e
            await resp.aclose()
            log.warning("%s backend: %s %s error body: %s", self.name, method, path,
                        data[:2000].decode("utf-8", "replace"))

            async def once():
                yield data

            async def noop():
                return None
            return BackendResponse(status=resp.status_code, content_type=ctype, body=once(), close=noop)
        return BackendResponse(status=resp.status_code, content_type=ctype, body=resp.aiter_bytes(), close=resp.aclose)

    async def aclose(self) -> None:
        await super().aclose()
        await self._client.aclose()
