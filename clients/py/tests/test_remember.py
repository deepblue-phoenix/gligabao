import base64
import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from gligabao import Client, GligabaoError


@pytest.fixture(autouse=True)
def _api_key(monkeypatch):
    monkeypatch.setenv("GLIGABAO_API_KEY", "test-key")


def _serve(status, payload, seen):
    class H(BaseHTTPRequestHandler):
        def do_POST(self):
            seen["path"] = self.path
            seen["headers"] = {k.lower(): v for k, v in self.headers.items()}
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


def test_remember_models():
    seen = {}
    srv = _serve(200, {"artifact_id": 1}, seen)
    try:
        Client(f"http://127.0.0.1:{srv.server_port}").remember("hi", models={"extract_graph": "m1"})
    finally:
        srv.shutdown()
    assert seen["body"] == {"text": "hi", "models": {"extract_graph": "m1"}}


def test_remember_web_search():
    seen = {}
    srv = _serve(200, {"artifact_id": 1}, seen)
    try:
        Client(f"http://127.0.0.1:{srv.server_port}").remember("hi", web_search=True)
    finally:
        srv.shutdown()
    assert seen["body"] == {"text": "hi", "web_search": True}
    with pytest.raises(ValueError):
        Client().remember(image=b"x", web_search=True)


def test_remember_timeseries():
    from datetime import datetime, timezone

    seen = {}
    srv = _serve(200, {"artifact_id": 1}, seen)
    t = datetime(2025, 1, 31, tzinfo=timezone.utc)
    try:
        c = Client(f"http://127.0.0.1:{srv.server_port}")
        c.remember("hi", timeseries=[{"key": "k", "points": [(t, 1.5), ("2025-02-01T00:00:00Z", 2)]}])
        assert seen["body"]["timeseries"] == {
            "series": [{"key": "k", "points": [["2025-01-31T00:00:00+00:00", 1.5], ["2025-02-01T00:00:00Z", 2]]}]
        }
        c.attach_timeseries(
            [{"key": "k", "points": [(t, 1)], "anchors": [{"artifact_id": 3, "window": [t, "2025-02-01T00:00:00Z"]}]}],
            dataset="d",
        )
    finally:
        srv.shutdown()
    assert seen["path"] == "/v1/gb/timeseries"
    assert seen["body"]["dataset"] == "d"
    assert seen["body"]["series"][0]["anchors"] == [
        {"artifact_id": 3, "window": ["2025-01-31T00:00:00+00:00", "2025-02-01T00:00:00Z"]}
    ]
    with pytest.raises(ValueError):
        Client().remember(image=b"x", timeseries=[{"key": "k", "points": []}])


def test_remember_observe_timeseries():
    seen = {}
    srv = _serve(200, {"artifact_id": 1}, seen)
    try:
        c = Client(f"http://127.0.0.1:{srv.server_port}")
        c.remember("hi", timeseries=[{"key": "k", "points": [("2025-02-01T00:00:00Z", 2)]}], observe_timeseries=True)
        assert seen["body"]["timeseries"]["observations"] is True
        c.remember("hi", fetch_timeseries=True, observe_timeseries=True)
        assert seen["body"]["timeseries"] == {"fetch": {}, "observations": True}
    finally:
        srv.shutdown()
    with pytest.raises(ValueError):
        Client().remember("hi", observe_timeseries=True)
    with pytest.raises(ValueError):
        Client().remember(image=b"x", observe_timeseries=True)


def test_remember():
    seen = {}
    srv = _serve(200, {"artifact_id": 1}, seen)
    try:
        res = Client(f"http://127.0.0.1:{srv.server_port}").remember("hi", dataset="d")
    finally:
        srv.shutdown()
    assert res == {"artifact_id": 1}
    assert seen["path"] == "/v1/gb/remember"
    assert seen["body"] == {"text": "hi", "dataset": "d"}


def test_remember_kind():
    seen = {}
    srv = _serve(200, {"artifact_id": 1}, seen)
    try:
        Client(f"http://127.0.0.1:{srv.server_port}").remember("hi", kind="episode")
    finally:
        srv.shutdown()
    assert seen["body"] == {"text": "hi", "kind": "episode"}


def test_remember_error():
    srv = _serve(400, {"error": "text is required"}, {})
    try:
        with pytest.raises(GligabaoError) as ei:
            Client(f"http://127.0.0.1:{srv.server_port}").remember("")
    finally:
        srv.shutdown()
    assert ei.value.status == 400
    assert "text is required" in str(ei.value)


def test_remember_image_bytes():
    seen = {}
    srv = _serve(200, {"image_artifact_id": 1, "theme": "plot"}, seen)
    try:
        res = Client(f"http://127.0.0.1:{srv.server_port}").remember(
            image=b"\x89PNG-bytes", dataset="d", reference_time="2025-01-31T09:00:00Z"
        )
    finally:
        srv.shutdown()
    assert res["theme"] == "plot"
    assert seen["path"] == "/v1/gb/remember"
    assert seen["body"] == {
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
        Client(f"http://127.0.0.1:{srv.server_port}").remember(image=str(f) if as_str else f)
    finally:
        srv.shutdown()
    assert seen["body"] == {"image": base64.b64encode(b"abc").decode()}


def test_remember_needs_exactly_one_of_text_and_image():
    c = Client("http://127.0.0.1:1")  # never contacted
    with pytest.raises(ValueError):
        c.remember()
    with pytest.raises(ValueError):
        c.remember("hi", image=b"x")
    with pytest.raises(ValueError):
        c.remember(image=b"x", kind="episode")


def test_query():
    seen = {}
    srv = _serve(200, {"answer": "Acme.", "evidence": {"entities": [], "edges": []}}, seen)
    try:
        res = Client(f"http://127.0.0.1:{srv.server_port}").query(
            "Where does Alice work?", dataset="d", session_id="s1", reasoning="low"
        )
    finally:
        srv.shutdown()
    assert seen["path"] == "/v1/gb/query"
    assert seen["body"] == {
        "query": "Where does Alice work?",
        "dataset": "d",
        "session_id": "s1",
        "reasoning": "low",
    }
    assert res["answer"] == "Acme."


def test_query_minimal():
    seen = {}
    srv = _serve(200, {"answer": "x"}, seen)
    try:
        Client(f"http://127.0.0.1:{srv.server_port}").query("q")
    finally:
        srv.shutdown()
    assert seen["body"] == {"query": "q"}


def test_api_key_from_env_is_sent_and_no_user_id():
    seen = {}
    srv = _serve(200, {"answer": "x"}, seen)
    try:
        Client(f"http://127.0.0.1:{srv.server_port}").query("q")
    finally:
        srv.shutdown()
    assert seen["headers"]["authorization"] == "Bearer test-key"
    assert "user_id" not in seen["body"]


def test_api_key_argument_wins_over_env():
    seen = {}
    srv = _serve(200, {"artifact_id": 1}, seen)
    try:
        Client(f"http://127.0.0.1:{srv.server_port}", api_key="other").remember("hi")
    finally:
        srv.shutdown()
    assert seen["headers"]["authorization"] == "Bearer other"


@pytest.mark.parametrize("value", [None, "", "  "])
def test_no_api_key(monkeypatch, value):
    if value is None:
        monkeypatch.delenv("GLIGABAO_API_KEY")
    else:
        monkeypatch.setenv("GLIGABAO_API_KEY", value)
    with pytest.raises(GligabaoError, match="GLIGABAO_API_KEY"):
        Client()


def test_unauthorized_is_an_error():
    seen = {}
    srv = _serve(401, {"error": "invalid API key"}, seen)
    try:
        with pytest.raises(GligabaoError, match="invalid API key") as e:
            Client(f"http://127.0.0.1:{srv.server_port}").query("q")
    finally:
        srv.shutdown()
    assert e.value.status == 401
