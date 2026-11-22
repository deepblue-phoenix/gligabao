#!/usr/bin/env bash
# Builds ./build/gligabao, ./build/gligabao_agent, ./build/gligabao_worker and ./build/gligabao_agent_linux_amd64. Usage: ./run_build.sh [--no-admin-ui] [--sargo]
#
# All build logic (npm, cargo, go) lives in ops/build.py, which has one pipeline per platform (Linux, macOS, Windows) and also runs directly:
#   python3 ops/build.py all --help
# This script only calls it. GOOS / GOARCH select the target (default: this machine), e.g. GOOS=linux GOARCH=arm64 ./run_build.sh --no-admin-ui.
#
# The admin UI (admin_ui/: TypeScript, SCSS, HTML) is built and embedded into the binary by default.
# Disable that with --no-admin-ui or BUILD_ADMIN_UI=false (then admin_ui = true in the config only logs a warning).
# Building the UI needs node + npm.
#
# With --sargo (or BUILD_SARGO=true) the sargo model router (rust/sargo) is built as a static binary and embedded into ./build/gligabao, which then
# runs it with `gligabao sargo serve`. Needs cargo (and musl-tools on Linux); sargo is built for the OS of this machine only (Rust does not cross-compile
# like Go does), and the embedded binary is for the architecture of the target.
#
# gligabao_agent is the deep-research agent with its sub-agents and the knowledge base in process (no server, API, training or evals), a small static binary
# for Lambdas and the like (docs/deep-research-agent.md). It does not embed the admin UI. gligabao_worker runs one remember or query pipeline in process
# and exits (docs/worker_binary.md): the same kind of small static binary. Both cross-compile (CGO is off, so no C toolchain is needed).
# gligabao_agent_linux_amd64 is the same agent for Linux on x86-64 whatever the host: it is what the sidecar bakes into the Modal app,
# to run research agents in Modal (runner "modal", docs/agents/research.md); the Modal app's deploy picks it up from build/.
set -euo pipefail
cd "$(dirname "$0")"

args=()
for arg in "$@"; do
  case "$arg" in
    --admin-ui|--no-admin-ui|--sargo) args+=("$arg") ;;
    *) echo "usage: $0 [--admin-ui|--no-admin-ui] [--sargo]" >&2; exit 2 ;;
  esac
done

exec "${PYTHON:-python3}" ops/build.py all ${args[@]+"${args[@]}"}
