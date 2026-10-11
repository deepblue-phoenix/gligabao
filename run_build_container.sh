#!/usr/bin/env bash
# Builds the server container image. Usage: ./run_build_container.sh [image[:tag]]
set -euo pipefail
cd "$(dirname "$0")"
IMAGE="${1:-gligabao:latest}"
docker build -t "$IMAGE" .
echo "Built image $IMAGE"
echo "Run: docker run --rm -p 7878:7878 -e ANTHROPIC_API_KEY -v gligabao-data:/data $IMAGE"
