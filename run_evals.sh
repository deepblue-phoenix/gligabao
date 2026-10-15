#!/usr/bin/env bash
# Runs the Go eval runner (evals/main.go).
# Usage: ./run_evals.sh [-s|--suite name] [-v] [case-name ...]  (-v: show LLM input/output; -h: help + suite list)
set -euo pipefail
cd "$(dirname "$0")"

usage() {
	cat <<USAGE
Usage: ./run_evals.sh [options] [case-name ...]

Runs the end-to-end evals against the real LLM (needs ANTHROPIC_API_KEY).

Options:
  -s, --suite NAME   run only the cases of the suite NAME (see the list below)
  -v                 show the input and output of every LLM call
  -h, --help         show this help
  (other options, e.g. -experiment, -models, -reasoning, -db, are passed to the runner: go run ./evals -h)

Case names restrict the run to those cases (combined with --suite, they must belong to that suite).

Eval suites (evals/suites/*.json):
USAGE
	go run ./evals -list-suites
}

args=()
while [ $# -gt 0 ]; do
	case "$1" in
	-h | --help | -help)
		usage
		exit 0
		;;
	-s | --suite | -suite)
		[ $# -ge 2 ] || { echo "error: $1 needs a suite name (see --help)" >&2; exit 2; }
		args+=(-suite "$2")
		shift 2
		;;
	--suite=* | -suite=*)
		args+=(-suite "${1#*=}")
		shift
		;;
	*)
		args+=("$1")
		shift
		;;
	esac
done

go run ./evals ${args[@]+"${args[@]}"}
