"""The routes the sidecar forwards to its backend (routes.py): the training and research jobs, unchanged.

    cd sidecar && python -m unittest test_backend_routes -v
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import httpx
from fastapi import FastAPI

from backends.base import Backend, BackendError, BackendResponse
from routes import ROUTES, add_backend_routes


class Recorder(Backend):
    name = "fake"

    def __init__(self):
        self.calls = []
        self.fail = False

    async def forward(self, method, path, query, body, content_type):
        self.calls.append((method, path, query, body))
        if self.fail:
            raise BackendError("backend down")

        async def chunks():
            yield b'{"ok": true}'

        async def close():
            pass
        return BackendResponse(202, "application/json", chunks(), close)


class BackendRoutesTest(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.backend = Recorder()
        app = FastAPI()
        add_backend_routes(app, self.backend)
        self.client = httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://sidecar")

    async def asyncTearDown(self):
        await self.client.aclose()

    async def test_research_routes_are_forwarded(self):
        self.assertIn(("POST", "/research/job_start"), ROUTES)
        self.assertIn(("GET", "/research/job_events"), ROUTES)
        self.assertIn(("POST", "/research/job_cancel"), ROUTES)
        self.assertIn(("GET", "/research/job_export"), ROUTES)
        r = await self.client.post("/research/job_start", content=b'{"topic": "t"}', headers={"content-type": "application/json"})
        self.assertEqual((r.status_code, r.json()), (202, {"ok": True}))   # the backend's answer, as it is
        await self.client.get("/research/job_events", params={"job_id": "j", "from": 3})
        await self.client.post("/research/job_cancel", json={"job_id": "j"})
        self.assertEqual([(m, p) for m, p, _, _ in self.backend.calls],
                         [("POST", "/research/job_start"), ("GET", "/research/job_events"), ("POST", "/research/job_cancel")])
        self.assertEqual(self.backend.calls[0][3], b'{"topic": "t"}')
        self.assertEqual(self.backend.calls[1][2], "job_id=j&from=3")

    async def test_an_unreachable_backend_is_a_502(self):
        self.backend.fail = True
        r = await self.client.post("/research/job_start", json={"topic": "t"})
        self.assertEqual((r.status_code, r.json()), (502, {"error": "backend down"}))


if __name__ == "__main__":
    unittest.main()
