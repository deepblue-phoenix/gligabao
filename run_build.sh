#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p bin
CGO_ENABLED=0 go build -o bin/gligabao .
echo "Built ./bin/gligabao"
