#!/usr/bin/env bash
# LOCAL/DEV ONLY (not for production): runs the server binary ./bin/gligabao (build it first with ./run_build.sh). Extra args are passed to `serve`.
# The admin UI is turned on (--admin-ui).
# The admin UI shows two databases: the standard one and the eval one (bin/gligabao-eval.db, written by ./run_evals.sh, never reset:
# it holds the evaluation datasets, the experiments and their runs; override the path with EVAL_DB=...).
# Run from the project root so the default DB path (bin/gligabao.db) resolves into bin/;
# the config is read from bin/gligabao.toml (next to the binary).
set -euo pipefail
cd "$(dirname "$0")"
[ -x bin/gligabao ] || { echo "bin/gligabao not found; run ./run_build.sh first" >&2; exit 1; }

# Start the sidecar (./run_server_sidecar_local.sh) if nothing answers on its health endpoint. The sidecar started here is
# stopped when this script exits or is killed (INT/TERM/HUP; SIGKILL cannot be trapped).
SIDECAR_URL="http://${SIDECAR_HOST:-127.0.0.1}:${SIDECAR_PORT:-8787}"
SIDECAR_PID=""
SERVER_PID=""
cleanup() {
  trap - EXIT INT TERM HUP
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  if [ -n "$SIDECAR_PID" ]; then
    echo "stopping local sidecar (pid $SIDECAR_PID)" >&2
    kill "$SIDECAR_PID" 2>/dev/null || true
    wait "$SIDECAR_PID" 2>/dev/null || true
  fi
  [ -n "$SERVER_PID" ] && wait "$SERVER_PID" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 143' INT TERM HUP

if curl -fs -m 2 "$SIDECAR_URL/health" >/dev/null 2>&1; then
  echo "sidecar already running at $SIDECAR_URL" >&2
else
  echo "sidecar not running at $SIDECAR_URL; starting it" >&2
  ./run_server_sidecar_local.sh &
  SIDECAR_PID=$!
  for _ in $(seq 1 60); do
    curl -fs -m 2 "$SIDECAR_URL/health" >/dev/null 2>&1 && break
    if ! kill -0 "$SIDECAR_PID" 2>/dev/null; then
      echo "warning: sidecar exited at startup (are MODAL_TOKEN_ID / MODAL_TOKEN_SECRET set?); continuing without it" >&2
      SIDECAR_PID=""
      break
    fi
    sleep 1
  done
fi

# Not exec'd, so the trap above can stop the sidecar when the server ends or this script is killed.
./bin/gligabao serve --admin-ui --eval-db "${EVAL_DB:-bin/gligabao-eval.db}" "$@" &
SERVER_PID=$!
rc=0
wait "$SERVER_PID" || rc=$?
SERVER_PID=""
exit "$rc"
