#!/usr/bin/env bash
# Builds the ML sidecar container image. Usage: ./run_build_ml_sidecar.sh [image[:tag]]
set -euo pipefail
cd "$(dirname "$0")"
IMAGE="${1:-gligabao-ml-sidecar:latest}"
docker build -f Dockerfile.ml_sidecar -t "$IMAGE" .
echo "Built image $IMAGE"
echo "Run: docker run --rm -p 127.0.0.1:8787:8787 -e MODAL_TOKEN_ID -e MODAL_TOKEN_SECRET $IMAGE"
