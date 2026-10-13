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
        """
        if (text is None) == (image is None):
            raise ValueError("remember takes exactly one of text and image")
        body: dict[str, Any] = {"user_id": user_id}
        if image is not None:
            if kind is not None:
                raise ValueError("kind does not apply to images")
            if web_search:
                raise ValueError("web_search does not apply to images")
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
