#!/usr/bin/env bash
# LOCAL/DEV ONLY (not for production): runs the server binary ./bin/gligabao (build it first with ./run_build.sh). Extra args are passed to `serve`.
# The admin UI is turned on (--admin-ui).
# Run from the project root so the default DB path (bin/gligabao.db) resolves into bin/;
# the config is read from bin/gligabao.toml (next to the binary).
set -euo pipefail
cd "$(dirname "$0")"
[ -x bin/gligabao ] || { echo "bin/gligabao not found; run ./run_build.sh first" >&2; exit 1; }
exec ./bin/gligabao serve --admin-ui "$@"
