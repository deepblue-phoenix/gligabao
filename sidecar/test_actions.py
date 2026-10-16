"""Tests of the backend actions (backends/actions.py) and their routes.

    cd sidecar && python -m unittest test_actions -v
"""
import asyncio
import json
import os
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import httpx
from fastapi import FastAPI

from backends.actions import ActionBusy, ActionNotRunning, ActionRunner, Stage
from backends.base import Backend, BackendError
from routes import add_action_routes

STAGES = (Stage("starting"), Stage("building", r"building"), Stage("creating", r"created"), Stage("done"))


def runner(script: str, **kw) -> ActionRunner:
    return ActionRunner("deploy", "Deploy", [sys.executable, "-u", "-c", script], cwd=os.getcwd(), stages=STAGES, **kw)


def parse_sse(chunks) -> list:
    out = []
    for block in b"".join(chunks).decode().split("\n\n"):
        fields = dict(l.split(": ", 1) for l in block.split("\n") if ": " in l and not l.startswith(":"))
        if "event" in fields:
            out.append((fields["event"], json.loads(fields["data"])))
    return out


class ActionRunnerTest(unittest.IsolatedAsyncioTestCase):
    async def test_success_log_and_stages(self):
        r = runner("print('hello'); print('building image x'); print('Created function f'); print('bye')")
        self.assertEqual(r.state()["state"], "idle")
        await r.start()
        events = parse_sse([c async for c in r.follow()])
        st = r.state()
        self.assertEqual((st["state"], st["stage"], st["exit_code"]), ("succeeded", "done", 0))
        lines = [l["line"] for ev, d in events if ev == "log" for l in d["lines"]]
        self.assertEqual(lines, ["hello", "building image x", "Created function f", "bye"])
        self.assertEqual(events[-1][0], "state")
        stages_seen = {d["stage"] for ev, d in events if ev == "state"}
        self.assertLessEqual({"building", "creating"} & stages_seen, {"building", "creating"})

    async def test_failure_keeps_stage_and_reports_exit(self):
        r = runner("import sys; print('building image'); print('boom', file=sys.stderr); sys.exit(3)")
        await r.start()
        async for _ in r.follow():
            pass
        st = r.state()
        self.assertEqual((st["state"], st["stage"], st["exit_code"]), ("failed", "building", 3))
        self.assertIn("boom", [l["line"] for l in r.snapshot()["log"]])      # stderr is merged

    async def test_carriage_returns_ansi_and_from(self):
        r = runner("import sys; sys.stdout.write('a\\r\\x1b[32mb\\x1b[0m\\rc\\n'); sys.stdout.write('d')")
        await r.start()
        async for _ in r.follow():
            pass
        self.assertEqual([l["line"] for l in r.snapshot()["log"]], ["a", "b", "c", "d"])
        self.assertEqual([l["i"] for l in r.snapshot(2)["log"]], [2, 3])

    async def test_busy_then_new_run_resets_log(self):
        r = runner("import time; print('x'); time.sleep(0.5)")
        await r.start()
        with self.assertRaises(ActionBusy):
            await r.start()
        async for _ in r.follow():
            pass
        first = r.state()["run_id"]
        await r.start()
        async for _ in r.follow():
            pass
        self.assertEqual(r.state()["run_id"], first + 1)
        self.assertEqual(len(r.snapshot()["log"]), 1)

    async def test_missing_command_fails_cleanly(self):
        r = ActionRunner("deploy", "Deploy", ["/nonexistent/cmd"], cwd=os.getcwd())
        await r.start()
        async for _ in r.follow():
            pass
        self.assertEqual(r.state()["state"], "failed")
        self.assertIn("could not run", r.state()["error"])

    async def test_max_lines_keeps_the_tail(self):
        r = runner("for i in range(20): print(i)", max_lines=5)
        await r.start()
        async for _ in r.follow():
            pass
        log = r.snapshot()["log"]
        self.assertEqual([l["line"] for l in log], ["15", "16", "17", "18", "19"])
        self.assertEqual(log[0]["i"], 15)

    async def test_cancel_interrupts_the_command(self):
        r = runner("import time\ntry:\n    print('started', flush=True); time.sleep(60)\nexcept KeyboardInterrupt:\n    print('cleaning up'); raise")
        with self.assertRaises(ActionNotRunning):
            await r.cancel()
        await r.start()
        while not any(l["line"] == "started" for l in r.snapshot()["log"]):
            await asyncio.sleep(0.05)
        await r.cancel()
        async for _ in r.follow():
            pass
        st = r.state()
        self.assertEqual((st["state"], st["error"]), ("cancelled", "cancelled"))
        self.assertIn("cleaning up", [l["line"] for l in r.snapshot()["log"]])   # SIGINT let it clean up
        with self.assertRaises(ActionNotRunning):
            await r.cancel()
        await r.start()                                                          # a cancelled action can be run again
        await r.cancel()
        async for _ in r.follow():
            pass
        self.assertEqual(r.state()["state"], "cancelled")

    async def test_cancel_kills_a_command_that_ignores_sigint(self):
        r = runner("import signal, time\nsignal.signal(signal.SIGINT, signal.SIG_IGN)\nprint('started', flush=True)\ntime.sleep(60)")
        await r.start()
        while not r.snapshot()["log"]:
            await asyncio.sleep(0.05)
        with mock.patch("backends.actions._CANCEL_GRACE_S", 0.3):
            await r.cancel()
            async for _ in r.follow():
                pass
        self.assertEqual(r.state()["state"], "cancelled")

    async def test_exclusive_actions_do_not_overlap(self):
        a, b = runner("import time; time.sleep(0.3)"), runner("print('x')")
        a.exclusive_with, b.exclusive_with = [b], [a]
        await a.start()
        with self.assertRaises(ActionBusy):
            await b.start()
        async for _ in a.follow():
            pass
        await b.start()                      # fine once a is over
        async for _ in b.follow():
            pass

    async def test_aclose_kills_the_process(self):
        r = runner("import time; time.sleep(60)")
        await r.start()
        await r.aclose()
        self.assertEqual(r.state()["state"], "failed")


class FakeBackend(Backend):
    name = "fake"

    def __init__(self):
        self._a = {"deploy": runner("print('hi')")}

    async def forward(self, *a, **k):
        raise NotImplementedError

    def actions(self):
        return self._a


class VolumesBackend(FakeBackend):
    name = "vols"

    async def volumes(self):
        return [{"name": "v1", "exists": True}]


class BrokenVolumesBackend(FakeBackend):
    name = "broken"

    async def volumes(self):
        raise BackendError("cannot list")


class ModalBackendTest(unittest.IsolatedAsyncioTestCase):
    async def test_actions_and_volumes_script(self):
        from backends.modal_backend import ModalBackend
        b = ModalBackend("app-x", token_id="ak-x", token_secret="as-x")
        self.assertEqual(set(b.actions()), {"deploy", "stop"})
        self.assertTrue(b.actions()["stop"].danger and "app-x" in b.actions()["stop"].confirm)
        with self.assertRaises(ActionBusy):          # deploy and stop exclude each other
            await b.actions()["deploy"].start()
            await b.actions()["stop"].start()
        await b.aclose()
        self.assertNotIn("stop", b.actions_status(skip=("stop",)))
        vols = await b.volumes()                     # fake credentials: every volume reports the auth error, nothing is created
        self.assertTrue(vols and all("name" in v and v["mount_path"] for v in vols))
        await b.aclose()


class AppsBackend(FakeBackend):
    name = "apps"

    def __init__(self):
        super().__init__()
        self._app = {"deploy": runner("print('app hi')")}

    def app_actions(self, app):
        return self._app if app == "a-1" else {}

    async def apps(self):
        return [{"name": "a-1", "actions": {}}]


class ModalAppsTest(unittest.IsolatedAsyncioTestCase):
    async def test_owned_apps_and_their_actions(self):
        from backends.modal_backend import ModalBackend
        b = ModalBackend("app-x")
        self.assertTrue(b.owns_app("app-x") and b.owns_app("app-x-dev"))
        self.assertFalse(b.owns_app("app-xy") or b.owns_app("other") or b.owns_app("app-x-../x") or b.owns_app(""))
        self.assertEqual(b.app_actions("other"), {})
        dev = b.app_actions("app-x-dev")
        self.assertIs(dev, b.app_actions("app-x-dev"))
        self.assertIsNot(dev["deploy"], b.actions()["deploy"])
        self.assertIn("app-x-dev", dev["stop"].confirm)
        self.assertEqual(dev["deploy"]._env["SIDECAR_APP_NAME"], "app-x-dev")   # the scripts act on that app
        self.assertEqual(b.actions()["deploy"]._env["SIDECAR_APP_NAME"], "app-x")

    async def test_apps_listing(self):
        from backends.modal_backend import ModalBackend
        b = ModalBackend("app-x")
        found = {"apps": [{"name": "app-x-dev", "app_id": "ap-1", "state": "deployed", "n_running_tasks": 0, "created_at": None},
                          {"name": "unrelated", "app_id": "ap-2", "state": "deployed", "n_running_tasks": 0, "created_at": None}]}
        with mock.patch.object(b, "_run_json", mock.AsyncMock(return_value=found)):
            apps = await b.apps()
        self.assertEqual([a["name"] for a in apps], ["app-x", "app-x-dev"])      # ours only, the configured one first
        self.assertEqual([a["active"] for a in apps], [False, True])             # the configured one is not deployed
        self.assertEqual(set(apps[0]["actions"]), {"deploy"})                    # nothing to stop
        self.assertEqual(set(apps[1]["actions"]), {"deploy", "stop"})
        await b.aclose()


class RoutesTest(unittest.IsolatedAsyncioTestCase):
    async def test_routes(self):
        app = FastAPI()
        backend = FakeBackend()
        add_action_routes(app, backend)
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://t") as c:
            self.assertEqual((await c.post("/backends/other/actions/deploy")).status_code, 404)
            self.assertEqual((await c.get("/backends/fake/actions/nope")).status_code, 404)
            self.assertEqual((await c.get("/backends/fake/actions/deploy")).json()["state"], "idle")
            self.assertEqual((await c.post("/backends/fake/actions/deploy/cancel")).status_code, 409)
            r = await c.post("/backends/fake/actions/deploy")
            self.assertEqual((r.status_code, r.json()["state"]), (202, "running"))
            ev = await c.get("/backends/fake/actions/deploy/events")
            self.assertTrue(ev.headers["content-type"].startswith("text/event-stream"))
            events = parse_sse([ev.content])
            self.assertEqual(events[-1][1]["state"], "succeeded")
            self.assertEqual((await c.get("/backends/fake/actions/deploy?from=0")).json()["log"][0]["line"], "hi")
        await backend.aclose()

    async def test_app_routes(self):
        app = FastAPI()
        backend = AppsBackend()
        add_action_routes(app, backend)
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://t") as c:
            self.assertEqual((await c.get("/backends/apps/apps")).json(), {"apps": [{"name": "a-1", "actions": {}}]})
            self.assertEqual((await c.get("/backends/apps/apps/b-2/actions/deploy")).status_code, 404)
            self.assertEqual((await c.get("/backends/other/apps/a-1/actions/deploy")).status_code, 404)
            self.assertEqual((await c.get("/backends/apps/apps/a-1/actions/deploy")).json()["state"], "idle")
            self.assertEqual((await c.post("/backends/apps/apps/a-1/actions/deploy")).status_code, 202)
            ev = await c.get("/backends/apps/apps/a-1/actions/deploy/events")
            self.assertEqual(parse_sse([ev.content])[-1][1]["state"], "succeeded")
            self.assertEqual((await c.get("/backends/apps/apps/a-1/actions/deploy")).json()["log"][0]["line"], "app hi")
            self.assertEqual((await c.get("/backends/apps/actions/deploy")).json()["state"], "idle")   # the backend's own app
            self.assertEqual((await c.get("/backends/fake/apps")).status_code, 404)
        await backend.aclose()

    async def test_volumes_route(self):
        for backend, want in ((VolumesBackend(), 200), (BrokenVolumesBackend(), 502), (FakeBackend(), 404)):
            app = FastAPI()
            add_action_routes(app, backend)
            async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://t") as c:
                r = await c.get(f"/backends/{backend.name}/volumes")
                self.assertEqual(r.status_code, want, r.text)
                self.assertEqual((await c.get("/backends/other/volumes")).status_code, 404)
                if want == 200:
                    self.assertEqual(r.json(), {"volumes": [{"name": "v1", "exists": True}]})


if __name__ == "__main__":
    unittest.main()
