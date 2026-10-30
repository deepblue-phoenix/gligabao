#!/usr/bin/env bash
# LOCAL/DEV ONLY: runs the compiled sargo server rust/sargo/target/release/sargo (build it first with ./run_build_rust.sh).
# Config via env vars: ANTHROPIC_API_KEY, OPENAI_API_KEY, SARGO_ADDR (default 127.0.0.1:8080),
# ANTHROPIC_BASE_URL, OPENAI_BASE_URL. Extra args are passed to the binary.
set -euo pipefail
cd "$(dirname "$0")"
BIN=rust/sargo/target/release/sargo
[ -x "$BIN" ] || { echo "$BIN not found; run ./run_build_rust.sh first" >&2; exit 1; }
exec "$BIN" "$@"
