#!/usr/bin/env bash
# Builds ./build/gligabao. Usage: ./run_build.sh [--no-admin-ui]
#
# The admin UI (admin_ui/: TypeScript, SCSS, HTML) is built and embedded into the binary by default.
# Disable that with --no-admin-ui or BUILD_ADMIN_UI=false (then admin_ui = true in the config only logs a warning).
# Building the UI needs node + npm.
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
