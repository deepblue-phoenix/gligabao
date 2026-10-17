"""Prints, as JSON on stdout, the Modal volumes the app uses (config.CONFIG.volumes: mount path -> volume name) and what
Modal knows about each: whether it exists, when it was created, how many entries its top level has.

Run by the sidecar (backends/modal_backend.py) as a module (`python -m ...`) with `sidecar/ai_train/` as cwd and the Modal credentials in the environment.
A volume that cannot be queried gets an "error" instead of failing the whole listing. Nothing is created or changed.
"""
import json
import sys

import modal
import modal.exception

from config import CONFIG


def describe(path: str, name: str) -> dict:
    out = {"mount_path": path, "name": name, "exists": None}
    try:
        vol = modal.Volume.from_name(name).hydrate()
    except modal.exception.NotFoundError:
        out["exists"] = False
        return out
    except Exception as e:
        out["error"] = f"{type(e).__name__}: {e}"
        return out
    out["exists"] = True
    try:
        info = vol.info()
        out["created_at"] = info.created_at.isoformat() if info.created_at else None
        out["entries"] = len(vol.listdir("/"))
    except Exception as e:
        out["error"] = f"{type(e).__name__}: {e}"
    return out


if __name__ == "__main__":
    json.dump({"volumes": [describe(p, n) for p, n in CONFIG.volumes.items()]}, sys.stdout)
