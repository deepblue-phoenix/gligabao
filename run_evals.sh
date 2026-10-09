#!/usr/bin/env bash
# Runs the Go eval runner (evals/main.go). Usage: ./run_evals.sh [-v] [case-name ...]  (-v: show LLM input/output)
set -euo pipefail
cd "$(dirname "$0")"
source ./cgo_env.sh
go run ./evals "$@"
