"""Deploys the Modal app programmatically (the SDK's `app.deploy()`, same as `modal deploy app.py` but without the CLI).

Run by the sidecar (backends/modal_backend.py) with this directory as cwd and the Modal credentials in the environment
(MODAL_TOKEN_ID / MODAL_TOKEN_SECRET); also fine by hand: `python deploy_modal.py`. Build output goes to stdout.
"""
import sys
import traceback

import modal

import app as train_app   # declares the functions; the app is `train_app.app`


def main() -> int:
    name = train_app.app.name
    print(f"Deploying Modal app {name!r} ...", flush=True)
    try:
        with modal.enable_output():
            train_app.app.deploy(name=name)
    except Exception as e:
        traceback.print_exc()
        print(f"Deploy failed: {type(e).__name__}: {e}", flush=True)
        return 1
    print(f"Deployed Modal app {name!r}.", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
