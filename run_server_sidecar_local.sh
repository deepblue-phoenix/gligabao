#!/usr/bin/env bash
# LOCAL/DEV ONLY: runs the Python sidecar (sidecar/main_sidecar.py) without a container, in the foreground.
# Does not create a venv or install anything: uses sidecar/.venv/bin/python if it exists, else python3 from PATH
# (set up the environment yourself: see docs/training/sidecar.md).
# Needs MODAL_TOKEN_ID and MODAL_TOKEN_SECRET in the environment. Listens on SIDECAR_HOST:SIDECAR_PORT (default 127.0.0.1:8787).
set -euo pipefail
cd "$(dirname "$0")/sidecar"
PY=python3
[ -x .venv/bin/python ] && PY=.venv/bin/python
# exec: the process keeps this PID, so a signal sent to it reaches the sidecar directly.
exec "$PY" main_sidecar.py "$@"
