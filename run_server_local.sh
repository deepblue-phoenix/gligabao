#!/usr/bin/env bash
# LOCAL/DEV ONLY (not for production): runs the server binary ./build/gligabao (build it first with ./run_build.sh). Extra args are passed to `serve`.
# The admin UI (--admin-ui) and the training endpoints (--training) are turned on.
# Run from the project root so the default DB path (build/gligabao.db) resolves into build/;
# the config is read from build/gligabao.toml (next to the binary).
set -euo pipefail
cd "$(dirname "$0")"
[ -x build/gligabao ] || { echo "build/gligabao not found; run ./run_build.sh first" >&2; exit 1; }

# Start the ML sidecar (./run_server_ml_sidecar_local.sh) if nothing answers on its health endpoint. The ML sidecar started here is
# stopped when this script exits or is killed (INT/TERM/HUP; SIGKILL cannot be trapped).
ML_SIDECAR_URL="http://${ML_SIDECAR_HOST:-127.0.0.1}:${ML_SIDECAR_PORT:-8787}"
ML_SIDECAR_PID=""
SERVER_PID=""
cleanup() {
  trap - EXIT INT TERM HUP
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  if [ -n "$ML_SIDECAR_PID" ]; then
    echo "stopping local ML sidecar (pid $ML_SIDECAR_PID)" >&2
    kill "$ML_SIDECAR_PID" 2>/dev/null || true
    wait "$ML_SIDECAR_PID" 2>/dev/null || true
  fi
  [ -n "$SERVER_PID" ] && wait "$SERVER_PID" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 143' INT TERM HUP

if curl -fs -m 2 "$ML_SIDECAR_URL/health" >/dev/null 2>&1; then
  echo "ml_sidecar already running at $ML_SIDECAR_URL" >&2
else
  echo "ml_sidecar not running at $ML_SIDECAR_URL; starting it" >&2
  ./run_server_ml_sidecar_local.sh &
  ML_SIDECAR_PID=$!
  for _ in $(seq 1 60); do
    curl -fs -m 2 "$ML_SIDECAR_URL/health" >/dev/null 2>&1 && break
    if ! kill -0 "$ML_SIDECAR_PID" 2>/dev/null; then
      echo "warning: ml_sidecar exited at startup (are MODAL_TOKEN_ID / MODAL_TOKEN_SECRET set?); continuing without it" >&2
      ML_SIDECAR_PID=""
      break
    fi
    sleep 1
  done
fi

# Not exec'd, so the trap above can stop the ML sidecar when the server ends or this script is killed.
./build/gligabao serve --training --admin-ui "$@" &
SERVER_PID=$!
rc=0
wait "$SERVER_PID" || rc=$?
SERVER_PID=""
exit "$rc"
