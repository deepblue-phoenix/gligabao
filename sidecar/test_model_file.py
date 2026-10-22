"""Tests of GET /models/file (routes.add_model_file_route) and the Modal backend's streaming of a model file
(backends/modal_backend.py), with a stand-in for the `providers.modal.read_model_file` subprocess. No Modal needed.

    cd sidecar && python -m unittest test_model_file -v
"""
import os
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import httpx
from fastapi import FastAPI

from backends.base import Backend, BackendResponse
from backends.modal_backend import ModalBackend
from routes import add_model_file_route

# Stands in for ai_train/providers/modal/read_model_file.py: `python -m providers.modal.read_model_file <model_path> <file>`.
FAKE_SCRIPT = textwrap.dedent('''
    import sys
    _, model_path, file = sys.argv
    if file == "bad/..":
        print("invalid model path or file", file=sys.stderr); sys.exit(2)
    if file == "missing":
        print("no file 'missing'", file=sys.stderr); sys.exit(3)
    if file == "boom":
        print("modal exploded", file=sys.stderr); sys.exit(1)
    if file == "empty":
        sys.exit(0)
    if file == "half":
        sys.stdout.buffer.write(b"x" * 10); sys.stdout.buffer.flush()
        print("died midway", file=sys.stderr); sys.exit(1)
    sys.stdout.buffer.write(b"A" * 3_000_000)   # more than one chunk
''')


class NoStorageBackend(Backend):
    name = "none"

    async def forward(self, *a, **k) -> BackendResponse:
        raise NotImplementedError


class ModelFileRouteTest(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        pkg = Path(self.tmp.name) / "providers" / "modal"
        pkg.mkdir(parents=True)
        (Path(self.tmp.name) / "providers" / "__init__.py").write_text("")
        (pkg / "__init__.py").write_text("")
        (pkg / "read_model_file.py").write_text(FAKE_SCRIPT)
        self.backend = ModalBackend("app", web_url="http://unused", ai_train_dir=Path(self.tmp.name))
        self.app = FastAPI()
        add_model_file_route(self.app, self.backend)

    def tearDown(self):
        self.tmp.cleanup()

    async def get(self, app, file, model_path="trained/j1"):
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://t") as c:
            return await c.get("/models/file", params={"model_path": model_path, "file": file})

    async def test_streams_the_file(self):
        r = await self.get(self.app, "adapter_model.safetensors")
        self.assertEqual((r.status_code, r.content), (200, b"A" * 3_000_000))
        self.assertEqual(r.headers["content-type"], "application/octet-stream")

    async def test_empty_file(self):
        r = await self.get(self.app, "empty")
        self.assertEqual((r.status_code, r.content), (200, b""))

    async def test_invalid_path_is_400(self):
        r = await self.get(self.app, "bad/..")
        self.assertEqual((r.status_code, r.json()["error"]), (400, "invalid model path or file"))

    async def test_missing_file_is_404(self):
        r = await self.get(self.app, "missing")
        self.assertEqual((r.status_code, r.json()["error"]), (404, "no file 'missing'"))

    async def test_other_failure_is_502(self):
        r = await self.get(self.app, "boom")
        self.assertEqual(r.status_code, 502)
        self.assertIn("modal exploded", r.json()["error"])

    async def test_failure_midway_aborts_the_response(self):
        with self.assertRaises(Exception):
            await self.get(self.app, "half")

    async def test_backend_without_storage_is_404(self):
        app = FastAPI()
        add_model_file_route(app, NoStorageBackend())
        r = await self.get(app, "adapter_model.safetensors")
        self.assertEqual(r.status_code, 404)


if __name__ == "__main__":
    unittest.main()
