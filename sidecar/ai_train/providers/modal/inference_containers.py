"""The warm inference containers of the Modal app: list them, or stop them (the app itself, its web API and any training
job keep running; the next inference request starts a new container and loads the model again).

Every inference container records itself in a Modal Dict (providers/modal_provider.py, `registry_name`) with its task id,
because Modal does not say which function a container runs. A record is only trusted while Modal still lists that task.

As a script (run by the sidecar, backends/modal_backend.py, as a module (`python -m ...`) with `sidecar/ai_train/` as cwd and the credentials in the
environment): `python -m providers.modal.inference_containers` stops the containers of the app named by SIDECAR_APP_NAME.
"""
import sys
import traceback

from modal._utils.async_utils import synchronizer
from modal.client import _Client
from modal.dict import _Dict
from modal.config import config
from modal.exception import NotFoundError
from modal_proto import api_pb2

from config import CONFIG
from providers.modal.provider import registry_name


def run_in_modal_loop(fn, *args):
    """Runs the coroutine function fn(*args) on the event loop of the Modal SDK and returns its result. The SDK's client
    and Dicts live on that loop: mixing them with a client made in a loop of our own (asyncio.run) hangs the Dict calls."""
    return synchronizer.create_blocking(fn, name=fn.__name__)(*args)


async def _live_tasks(client, app_name: str) -> set:
    """Task ids Modal lists as running for the app."""
    env = config.get("environment") or ""
    resp = await client.stub.AppGetByDeploymentName(api_pb2.AppGetByDeploymentNameRequest(name=app_name, environment_name=env))
    if not resp.app_id:
        return set()
    tasks = await client.stub.TaskList(api_pb2.TaskListRequest(environment_name=env))
    return {t.task_id for t in tasks.tasks if t.app_id == resp.app_id}


async def list_containers(app_name: str, client=None) -> list:
    """The warm inference containers of an app: [{"task_id", "function", "model_size", "adapter", "loaded_at",
    "last_used", "requests"}, ...], newest first. Records of containers that are gone are removed."""
    client = client or await _Client.from_env()
    try:
        registry = _Dict.from_name(registry_name(app_name))
        records = [(k, v) async for k, v in registry.items()]
    except NotFoundError:    # no container ever recorded itself
        return []
    live = await _live_tasks(client, app_name)
    out = []
    for task_id, info in records:
        if task_id in live:
            out.append({"task_id": task_id, **(info if isinstance(info, dict) else {})})
        else:
            await registry.pop(task_id, None)
    return sorted(out, key=lambda c: c.get("loaded_at") or 0, reverse=True)


async def stop(app_name: str) -> int:
    client = await _Client.from_env()
    containers = await list_containers(app_name, client)
    if not containers:
        print(f"No inference container is running in Modal app {app_name!r}: nothing to stop.", flush=True)
        return 0
    registry = _Dict.from_name(registry_name(app_name))
    failed = 0
    for c in containers:
        label = f"{c['task_id']} ({c.get('function', '?')}, {c.get('adapter') or c.get('model_size', '?')})"
        print(f"Stopping inference container {label} ...", flush=True)
        try:
            await client.stub.ContainerStop(api_pb2.ContainerStopRequest(task_id=c["task_id"]))
            await registry.pop(c["task_id"], None)
        except Exception as e:
            failed += 1
            print(f"Could not stop {c['task_id']}: {type(e).__name__}: {e}", flush=True)
    print(f"Stopped {len(containers) - failed} of {len(containers)} inference container(s).", flush=True)
    return 1 if failed else 0


def main() -> int:
    try:
        return run_in_modal_loop(stop, CONFIG.app_name)
    except Exception as e:
        traceback.print_exc()
        print(f"Stop failed: {type(e).__name__}: {e}", flush=True)
        return 1


if __name__ == "__main__":
    sys.exit(main())
