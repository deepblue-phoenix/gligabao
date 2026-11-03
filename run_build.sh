#!/usr/bin/env bash
# Builds ./build/gligabao, ./build/gligabao_agent, ./build/gligabao_lambda and ./build/gligabao_agent_linux_amd64. Usage: ./run_build.sh [--no-admin-ui]
#
# The admin UI (admin_ui/: TypeScript, SCSS, HTML) is built and embedded into the binary by default.
# Disable that with --no-admin-ui or BUILD_ADMIN_UI=false (then admin_ui = true in the config only logs a warning).
# Building the UI needs node + npm.
#
# gligabao_agent is the deep-research agent with its sub-agents and the knowledge base in process (no server, API, training or evals), a small static binary
# for Lambdas and the like (docs/deep-research-agent.md). It does not embed the admin UI. Cross-compile it as any Go program, e.g.
# GOOS=linux GOARCH=arm64 ./run_build.sh --no-admin-ui (CGO is off, so it needs no C toolchain).
# gligabao_lambda runs one remember, query or research in process and exits (docs/lambda.md): the same kind of small static binary, for
# Lambdas and one-shot jobs. Cross-compile it the same way (GOOS=linux GOARCH=arm64 ./run_build.sh --no-admin-ui).
# gligabao_agent_linux_amd64 is the same binary for Linux on x86-64 whatever the host: it is what the sidecar bakes into the Modal app,
# to run research agents in Modal (runner "modal", docs/research.md); the Modal app's deploy picks it up from build/.
set -euo pipefail
cd "$(dirname "$0")"

BUILD_ADMIN_UI="${BUILD_ADMIN_UI:-true}"
for arg in "$@"; do
  case "$arg" in
    --admin-ui) BUILD_ADMIN_UI=true ;;
    --no-admin-ui) BUILD_ADMIN_UI=false ;;
    *) echo "usage: $0 [--admin-ui|--no-admin-ui]" >&2; exit 2 ;;
  esac
done

if [ "$BUILD_ADMIN_UI" = true ]; then
  (
    cd admin_ui
    [ -d node_modules ] || npm ci
    npm run build
  )
else
  # Drop stale artifacts so they are not embedded; keep the .gitkeep placeholder.
  find admin_ui/dist -mindepth 1 -not -name .gitkeep -delete 2>/dev/null || true
fi

mkdir -p build
go build -o build/gligabao ./cmd/gligabao
echo "Built ./build/gligabao (admin UI embedded: $BUILD_ADMIN_UI)"

# The agent binary: pure Go, stripped, with the version from VERSION (it cannot embed it, VERSION is outside its package).
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(tr -d '[:space:]' < VERSION)" -o build/gligabao_agent ./cmd/gligabao_agent
echo "Built ./build/gligabao_agent"

# The lambda binary: remember, query and research in process, one per run (pure Go, stripped, version from VERSION).
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(tr -d '[:space:]' < VERSION)" -o build/gligabao_lambda ./cmd/gligabao_lambda
echo "Built ./build/gligabao_lambda"

# The same for the Modal containers (Linux, x86-64), whatever this machine is. The sidecar's deploy bakes it into the Modal app.
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(tr -d '[:space:]' < VERSION)" -o build/gligabao_agent_linux_amd64 ./cmd/gligabao_agent
echo "Built ./build/gligabao_agent_linux_amd64"
