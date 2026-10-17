"""Stops the deployed Modal app (what `modal app stop <name>` does, without the CLI): its running containers (training
jobs included) are terminated and its web endpoints go away until it is deployed again. Volumes are not touched.

Run by the sidecar (backends/modal_backend.py) as a module (`python -m ...`) with `sidecar/ai_train/` as cwd and the Modal credentials in the environment.
Uses the same two calls as the CLI (the SDK has no public `App.stop` yet): look the app up by name, then AppStop.
"""
import asyncio
import sys
import traceback

from modal.client import _Client
from modal.config import config
from modal_proto import api_pb2

from config import CONFIG


async def stop(name: str) -> int:
    client = await _Client.from_env()
    env = config.get("environment") or ""
    resp = await client.stub.AppGetByDeploymentName(api_pb2.AppGetByDeploymentNameRequest(name=name, environment_name=env))
    if not resp.app_id:
        print(f"No deployed Modal app named {name!r}: nothing to stop.", flush=True)
        return 1
    print(f"Stopping Modal app {name!r} ({resp.app_id}) ...", flush=True)
    await client.stub.AppStop(api_pb2.AppStopRequest(app_id=resp.app_id, source=api_pb2.APP_STOP_SOURCE_PYTHON_CLIENT))
    print(f"Stopped Modal app {name!r}.", flush=True)
    return 0


def main() -> int:
    try:
        return asyncio.run(stop(CONFIG.app_name))
    except Exception as e:
        traceback.print_exc()
        print(f"Stop failed: {type(e).__name__}: {e}", flush=True)
        return 1


if __name__ == "__main__":
    sys.exit(main())
