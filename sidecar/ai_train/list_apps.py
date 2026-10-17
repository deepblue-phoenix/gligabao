"""Prints, as JSON on stdout, the Modal apps of the workspace that are active (deployed, running or starting) and whose
name is the configured app (SIDECAR_APP_NAME) or starts with it plus "-": what `modal app list` shows, restricted to this
system. Run by the sidecar (backends/modal_backend.py) with this directory as cwd and the Modal credentials in the
environment. Read only.
"""
import asyncio
import json
import sys

from modal.client import _Client
from modal.config import config
from modal_proto import api_pb2

from config import CONFIG
from inference_containers import list_containers

# States of an app that is up (the CLI's table: deployed, ephemeral / detached, initializing); stopped ones are history.
ACTIVE = {
    api_pb2.APP_STATE_DEPLOYED: "deployed",
    api_pb2.APP_STATE_DETACHED: "ephemeral",
    api_pb2.APP_STATE_DETACHED_DISCONNECTED: "ephemeral",
    api_pb2.APP_STATE_EPHEMERAL: "ephemeral",
    api_pb2.APP_STATE_INITIALIZING: "initializing",
}


def iso(ts: float):
    from datetime import datetime, timezone
    return datetime.fromtimestamp(ts, timezone.utc).isoformat() if ts else None


async def list_apps(prefix: str) -> list:
    client = await _Client.from_env()
    resp = await client.stub.AppList(api_pb2.AppListRequest(environment_name=config.get("environment") or ""))
    out = []
    for a in resp.apps:
        name = a.name or a.description
        if a.state in ACTIVE and (name == prefix or name.startswith(prefix + "-")):
            try:   # the warm inference containers (the Dict may not exist yet, or Modal may be slow: then none are shown)
                inference = await list_containers(name, client) if a.state == api_pb2.APP_STATE_DEPLOYED else []
            except Exception:
                inference = []
            out.append({"name": name, "app_id": a.app_id, "state": ACTIVE[a.state],
                        "n_running_tasks": a.n_running_tasks, "created_at": iso(a.created_at), "inference": inference})
    return out


if __name__ == "__main__":
    json.dump({"apps": asyncio.run(list_apps(CONFIG.app_name))}, sys.stdout)
