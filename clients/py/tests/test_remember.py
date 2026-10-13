import base64
import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from gligabao import Client, GligabaoError


def _serve(status, payload, seen):
    class H(BaseHTTPRequestHandler):
        def do_POST(self):
            seen["path"] = self.path
            seen["body"] = json.loads(self.rfile.read(int(self.headers["content-length"])))
            out = json.dumps(payload).encode()
            self.send_response(status)
            self.send_header("content-length", str(len(out)))
            self.end_headers()
            self.wfile.write(out)

        def log_message(self, *a):
            pass

    srv = HTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def test_remember():
    seen = {}
    srv = _serve(200, {"artifact_id": 1}, seen)
    try:
        res = Client(f"http://127.0.0.1:{srv.server_port}").remember("u1", "hi", dataset="d")
    finally:
        srv.shutdown()
    assert res == {"artifact_id": 1}
    assert seen["path"] == "/v1/gb/remember"
    assert seen["body"] == {"user_id": "u1", "text": "hi", "dataset": "d"}


def test_remember_kind():
    seen = {}
    srv = _serve(200, {"artifact_id": 1}, seen)
    try:
        Client(f"http://127.0.0.1:{srv.server_port}").remember("u1", "hi", kind="episode")
    finally:
        srv.shutdown()
    assert seen["body"] == {"user_id": "u1", "text": "hi", "kind": "episode"}


def test_remember_error():
    srv = _serve(400, {"error": "text is required"}, {})
    try:
        with pytest.raises(GligabaoError) as ei:
            Client(f"http://127.0.0.1:{srv.server_port}").remember("u1", "")
    finally:
        srv.shutdown()
    assert ei.value.status == 400
    assert "text is required" in str(ei.value)


def test_remember_image_bytes():
    seen = {}
    srv = _serve(200, {"image_artifact_id": 1, "theme": "plot"}, seen)
    try:
        res = Client(f"http://127.0.0.1:{srv.server_port}").remember(
            "u1", image=b"\x89PNG-bytes", dataset="d", reference_time="2025-01-31T09:00:00Z"
        )
    finally:
        srv.shutdown()
    assert res["theme"] == "plot"
    assert seen["path"] == "/v1/gb/remember"
    assert seen["body"] == {
        "user_id": "u1",
        "image": base64.b64encode(b"\x89PNG-bytes").decode(),
        "dataset": "d",
        "reference_time": "2025-01-31T09:00:00Z",
    }


@pytest.mark.parametrize("as_str", [False, True])
def test_remember_image_path(tmp_path, as_str):
    f = tmp_path / "a.png"
    f.write_bytes(b"abc")
    seen = {}
    srv = _serve(200, {"image_artifact_id": 1}, seen)
    try:
        Client(f"http://127.0.0.1:{srv.server_port}").remember("u1", image=str(f) if as_str else f)
    finally:
        srv.shutdown()
    assert seen["body"] == {"user_id": "u1", "image": base64.b64encode(b"abc").decode()}


def test_remember_needs_exactly_one_of_text_and_image():
    c = Client("http://127.0.0.1:1")  # never contacted
    with pytest.raises(ValueError):
        c.remember("u1")
    with pytest.raises(ValueError):
        c.remember("u1", "hi", image=b"x")
    with pytest.raises(ValueError):
        c.remember("u1", image=b"x", kind="episode")
