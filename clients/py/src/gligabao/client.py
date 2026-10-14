from __future__ import annotations

import base64
import json
import os
import urllib.error
import urllib.request
from datetime import datetime
from typing import Any

from .errors import GligabaoError

DEFAULT_BASE_URL = "http://127.0.0.1:7878"


class Client:
    """Client for the gligabao HTTP API."""

    def __init__(self, base_url: str = DEFAULT_BASE_URL, timeout: float = 120.0):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    def remember(
        self,
        user_id: str,
        text: str | None = None,
        dataset: str | None = None,
        reference_time: datetime | str | None = None,
        kind: str | None = None,
        image: bytes | str | os.PathLike[str] | None = None,
        models: dict[str, str] | None = None,
        web_search: bool = False,
        timeseries: list[dict[str, Any]] | None = None,
    ) -> dict[str, Any]:
        """Store `text`, or an `image`, in memory (POST /v1/gb/remember).

        Give exactly one of `text` and `image`.

        `dataset` defaults to "main" on the server. `reference_time` (a datetime or an
        RFC 3339 string) is when the text was written, or the image captured; it defaults
        to now on the server.

        With `text`, `kind` is the artifact kind: "knowledge" (the server default, not
        strictly episodic) or "episode" (a Graphiti-style episode, anchored in time).
        Returns the decoded JSON response (artifact_id, entities, edges, ...).

        With `image` (the raw bytes of a JPEG, PNG, GIF or WebP up to 5 MiB, or the path of
        such a file as a str or Path), the server runs the image pipeline: an LLM reads the
        text in the image and classifies it by theme. `kind` does not apply to images.
        Returns the decoded JSON response (image_artifact_id, extraction_id, theme, title,
        text, text_blocks, ...).

        `models` picks the LLM model of each stage of the pipeline, e.g.
        {"extract_graph": "claude-sonnet-4-5", "resolve_edges": "claude-haiku-5-5"}. The stages are
        "extract_graph" (text), "resolve_edges" (text) and "extract_image" (image); a stage left out uses the
        server's default model. The model of "extract_graph" is the one recorded on the entities
        and edges the text yields. "web_search" is a further stage: the model that runs the web search.

        `web_search=True` (text only) also searches the web for the latest world events about the key
        entities of the text, and remembers what it finds as artifacts of kind "web_search" (the response
        lists them under "web_searches"). The server needs an Anthropic API key for it, else it answers 503.

        `timeseries` (text only) is a list of series to store and anchor to what the text yields, each a dict
        like {"key": "AAPL.close", "unit": "USD", "points": [(datetime, 235.1), ...], "anchors": [{"entity":
        {"name": "Apple", "type": "organization"}}]}. A point is a (time, value) pair, the time a datetime or an
        RFC 3339 string. A series without "anchors" is anchored to the artifact. Entity and edge anchors are
        matched after the text is extracted, so they may name entities the text will yield. The response lists
        the stored series under "series". See docs/timeseries.md.
        """
        if (text is None) == (image is None):
            raise ValueError("remember takes exactly one of text and image")
        body: dict[str, Any] = {"user_id": user_id}
        if image is not None:
            if kind is not None:
                raise ValueError("kind does not apply to images")
            if web_search:
                raise ValueError("web_search does not apply to images")
            if timeseries:
                raise ValueError("timeseries does not apply to images")
            if not isinstance(image, (bytes, bytearray, memoryview)):
                with open(image, "rb") as f:
                    image = f.read()
            body["image"] = base64.b64encode(bytes(image)).decode("ascii")
        else:
            body["text"] = text
        if dataset is not None:
            body["dataset"] = dataset
        if kind is not None:
            body["kind"] = kind
        if models:
            body["models"] = models
        if web_search:
            body["web_search"] = True
        if timeseries:
            body["timeseries"] = {"series": _series(timeseries)}
        if reference_time is not None:
            body["reference_time"] = (
                reference_time.isoformat() if isinstance(reference_time, datetime) else reference_time
            )
        return self._request("POST", "/v1/gb/remember", body)

    def attach_timeseries(
        self,
        user_id: str,
        series: list[dict[str, Any]],
        dataset: str | None = None,
    ) -> dict[str, Any]:
        """Store timeseries in an existing dataset and anchor them (POST /v1/gb/timeseries).

        `series` is as for `remember(timeseries=...)`, but there is no artifact to anchor to, so anchors name
        what exists: {"entity_id": 12}, {"artifact_id": 1, "window": [start, end]}, or an entity or edge
        selector. A series with the key of a stored one adds its points to it. The dataset must exist (404).
        """
        body: dict[str, Any] = {"user_id": user_id, "series": _series(series)}
        if dataset is not None:
            body["dataset"] = dataset
        return self._request("POST", "/v1/gb/timeseries", body)

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


def _time(t: datetime | str) -> str:
    return t.isoformat() if isinstance(t, datetime) else t


def _series(series: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """The series as the API takes them: points as [time, value] pairs with RFC 3339 times."""
    out = []
    for s in series:
        s = dict(s)
        s["points"] = [[_time(t), v] for t, v in s.get("points", [])]
        anchors = []
        for a in s.get("anchors", []):
            a = dict(a)
            if "window" in a:
                a["window"] = [_time(t) for t in a["window"]]
            anchors.append(a)
        if anchors:
            s["anchors"] = anchors
        out.append(s)
    return out
