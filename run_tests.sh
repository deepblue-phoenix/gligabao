#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
CGO_ENABLED=0 go test -count=1 ./...
