#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
source ./cgo_env.sh
go test -count=1 ./...
