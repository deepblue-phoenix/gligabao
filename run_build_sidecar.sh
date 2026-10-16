#!/usr/bin/env bash
# Builds the sidecar container image. Usage: ./run_build_sidecar.sh [image[:tag]]
set -euo pipefail
cd "$(dirname "$0")"
IMAGE="${1:-gligabao-sidecar:latest}"
docker build -f Dockerfile.sidecar -t "$IMAGE" .
echo "Built image $IMAGE"
echo "Run: docker run --rm -p 127.0.0.1:8787:8787 $IMAGE"
