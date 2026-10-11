#!/usr/bin/env bash
# Runs the server binary ./bin/gligabao (build it first with ./run_build.sh). Extra args are passed to `serve`.
# Run from the project root so the default DB path (bin/gligabao.db) resolves into bin/;
# the config is read from bin/gligabao.toml (next to the binary).
set -euo pipefail
cd "$(dirname "$0")"
[ -x bin/gligabao ] || { echo "bin/gligabao not found; run ./run_build.sh first" >&2; exit 1; }
exec ./bin/gligabao serve "$@"
