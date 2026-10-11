from __future__ import annotations

import json
import urllib.error
import urllib.request
from datetime import datetime
from typing import Any

from .errors import GligabaoError

DEFAULT_BASE_URL = "http://127.0.0.1:8080"


class Client:
    """Client for the gligabao HTTP API."""

    def __init__(self, base_url: str = DEFAULT_BASE_URL, timeout: float = 120.0):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    def remember(
        self,
        user_id: str,
        text: str,
        dataset: str | None = None,
        reference_time: datetime | str | None = None,
        kind: str | None = None,
    ) -> dict[str, Any]:
        """Store `text` in memory (POST /v1/gb/remember).

        `dataset` defaults to "main" on the server. `reference_time` (a datetime or an
        RFC 3339 string) is when the text was written; it defaults to now on the server.
        `kind` is the artifact kind: "knowledge" (the server default, not strictly episodic) or
        "episode" (a Graphiti-style episode, anchored in time).
        Returns the decoded JSON response (artifact_id, entities, edges, ...).
        """
        body: dict[str, Any] = {"user_id": user_id, "text": text}
        if dataset is not None:
            body["dataset"] = dataset
        if kind is not None:
            body["kind"] = kind
        if reference_time is not None:
            body["reference_time"] = (
                reference_time.isoformat() if isinstance(reference_time, datetime) else reference_time
            )
        return self._request("POST", "/v1/gb/remember", body)

    def _request(self, method: str, path: str, body: dict[str, Any] | None = None) -> Any:
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(
            self.base_url + path,
            data=data,
            method=method,
            headers={"content-type": "application/json", "accept": "application/json"},
        )
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                return json.loads(resp.read())
        except urllib.error.HTTPError as e:
            raw = e.read().decode(errors="replace")
            try:
                msg = json.loads(raw)["error"]
            except (ValueError, KeyError, TypeError):
                msg = raw or e.reason
            raise GligabaoError(msg, status=e.code) from None
        except urllib.error.URLError as e:
            raise GligabaoError(f"cannot reach {self.base_url}: {e.reason}") from e
