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
exec ./bin/gligabao serve --admin-ui --eval-db "${EVAL_DB:-bin/gligabao-eval.db}" "$@"
