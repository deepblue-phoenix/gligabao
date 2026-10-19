#!/usr/bin/env bash
# Runs a Go eval runner: the knowledge evals (evals/knowledge/main.go, default) or the training evals (evals/training/cmd/main.go).
# Usage: ./run_evals.sh [-t|--type knowledge|training] [-e|--eval name] [-v] [case-name ...]  (-h: help + eval list)
set -euo pipefail
cd "$(dirname "$0")"

usage() {
	cat <<USAGE
Usage: ./run_evals.sh [options] [case-name ...]

Runs one of the two kinds of evals against the real LLM (needs ANTHROPIC_API_KEY):
  knowledge   (default) knowledge extraction: remember, then recall
  training    a trained model answers the cases of an eval (questions about knowledge in its training data, phrased
              differently from the training examples), an LLM judges the answers; the run is stored in the database, as the
              admin UI's Eval button does it (-no-store to skip). Needs a running sidecar and the id of a trained model:
              -model ID (see ./run_evals.sh -t training -list-models); without -e the eval is the one named like its dataset

Options:
  -t, --type KIND    which evals to run: knowledge (default) or training
  -e, --eval NAME    run only the cases of the eval NAME (see the list below)
  -v                 knowledge: show the input and output of every LLM call; training: show every question and answer
  -h, --help         show this help
  (other options are passed to the runner: knowledge: -experiment, -models, -reasoning, -db (go run ./evals/knowledge -h);
   training: -model, -list-models, -db, -sidecar, -batch-size, -judge-model (go run ./evals/training/cmd -h))

Case names restrict the run to those cases (combined with --eval, they must belong to that eval).

Knowledge evals (bin/evals_knowledge/*.json):
USAGE
	go run ./evals/knowledge -list-evals
	echo
	echo "Training evals (bin/evals_training/*.json):"
	go run ./evals/training/cmd -list-evals
}

kind=knowledge

args=()
while [ $# -gt 0 ]; do
	case "$1" in
	-h | --help | -help)
		usage
		exit 0
		;;
	-t | --type | -type)
		[ $# -ge 2 ] || { echo "error: $1 needs knowledge or training" >&2; exit 2; }
		kind="$2"
		shift 2
		;;
	--type=* | -type=*)
		kind="${1#*=}"
		shift
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

case "$kind" in
knowledge) go run ./evals/knowledge ${args[@]+"${args[@]}"} ;;
training) go run ./evals/training/cmd ${args[@]+"${args[@]}"} ;;
*)
	echo "error: unknown eval type \"$kind\" (knowledge or training)" >&2
	exit 2
	;;
esac
