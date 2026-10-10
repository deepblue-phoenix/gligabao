#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p bin
go build -o bin/gligabao ./cmd/gligabao
echo "Built ./bin/gligabao"
