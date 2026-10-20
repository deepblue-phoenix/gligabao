#!/usr/bin/env bash
# Runs the Go tests, then the Python tests (sidecar and Python client).
# Usage: ./run_tests.sh [-g|--go] [-p|--py]
# No flags: run both. With flags: run only the selected ones.
set -euo pipefail
cd "$(dirname "$0")"

usage() {
	cat <<USAGE
Usage: ./run_tests.sh [options]

  -g, --go     run the Go tests
  -p, --py     run the Python tests (sidecar, clients/py)
  -h, --help   show this help

Without options both run (Go first, then Python). With options only the selected ones run.
Set PYTHON to choose the interpreter (default: python3).
USAGE
}

run_go=0
run_py=0
for arg in "$@"; do
	case "$arg" in
	-g | --go) run_go=1 ;;
	-p | --py) run_py=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "unknown option: $arg" >&2
		usage >&2
		exit 2
		;;
	esac
done
if [ "$run_go" = 0 ] && [ "$run_py" = 0 ]; then
	run_go=1
	run_py=1
fi

PYTHON="${PYTHON:-python3}"

if [ "$run_go" = 1 ]; then
	echo "==> Go tests"
	go test -count=1 ./...
fi

if [ "$run_py" = 1 ]; then
	echo "==> Python tests: sidecar"
	(cd sidecar && "$PYTHON" -m pytest -q .)
	echo "==> Python tests: clients/py"
	(cd clients/py && PYTHONPATH=src "$PYTHON" -m pytest -q)
fi
