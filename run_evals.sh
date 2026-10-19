#!/usr/bin/env bash
# Runs the Go eval runner (evals/knowledge/main.go).
# Usage: ./run_evals.sh [-e|--eval name] [-v] [case-name ...]  (-v: show LLM input/output; -h: help + eval list)
set -euo pipefail
cd "$(dirname "$0")"

usage() {
	cat <<USAGE
Usage: ./run_evals.sh [options] [case-name ...]

Runs the end-to-end evals against the real LLM (needs ANTHROPIC_API_KEY).

Options:
  -e, --eval NAME    run only the cases of the eval NAME (see the list below)
  -v                 show the input and output of every LLM call
  -h, --help         show this help
  (other options, e.g. -experiment, -models, -reasoning, -db, are passed to the runner: go run ./evals/knowledge -h)

Case names restrict the run to those cases (combined with --eval, they must belong to that eval).

Evals (evals/knowledge/evals/*.json):
USAGE
	go run ./evals/knowledge -list-evals
}

args=()
while [ $# -gt 0 ]; do
	case "$1" in
	-h | --help | -help)
		usage
		exit 0
		;;
	-e | --eval | -eval)
		[ $# -ge 2 ] || { echo "error: $1 needs an eval name (see --help)" >&2; exit 2; }
		args+=(-eval "$2")
		shift 2
		;;
	--eval=* | -eval=*)
		args+=(-eval "${1#*=}")
		shift
		;;
	*)
		args+=("$1")
		shift
		;;
	esac
done

go run ./evals/knowledge ${args[@]+"${args[@]}"}
