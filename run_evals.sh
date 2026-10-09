#!/usr/bin/env bash
# Runs the Go eval runner (evals/main.go). Usage: ./run_evals.sh [case-name ...]
set -euo pipefail
cd "$(dirname "$0")"
CGO_ENABLED=0 go run ./evals "$@"
