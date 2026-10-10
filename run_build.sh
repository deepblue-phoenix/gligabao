#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
source ./cgo_env.sh
mkdir -p bin
go build -o bin/gligabao ./cmd/gligabao
echo "Built ./bin/gligabao"
