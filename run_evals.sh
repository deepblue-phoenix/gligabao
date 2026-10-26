#!/usr/bin/env bash
# Runs a Go eval runner: the knowledge evals (internal/evals/knowledge/cmd, default) or the training evals (internal/evals/training/cmd).
# Usage: ./run_evals.sh [-t|--type knowledge|training] [-e|--eval name] [-v] [case-name ...]  (-h: help + eval list)
set -euo pipefail
cd "$(dirname "$0")"

# The config file whose evals_knowledge_dirs / evals_training_dirs list the eval directories (relative paths in it are relative to the
# file). Override with GLIGABAO_CONFIG; a -evals DIR option (repeatable) still overrides the config.
CONFIG="${GLIGABAO_CONFIG:-bin/gligabao.toml}"

usage() {
	cat <<USAGE
Usage: ./run_evals.sh [options] [case-name ...]

Runs one of the two kinds of evals against the real LLM (needs ANTHROPIC_API_KEY):
  knowledge   (default) knowledge extraction: remember, then query
  training    a trained model answers the cases of an eval (questions about knowledge in its training data, phrased
              differently from the training examples), an LLM judges the answers; the run is stored in the database, as the
              admin UI's Eval button does it. Needs a running sidecar and the id of a trained model:
              -model ID (see ./run_evals.sh -t training -list-models); without -e the eval is the one named like its dataset

Options:
  -t, --type KIND    which evals to run: knowledge (default) or training
  -e, --eval NAME    run only the cases of the eval NAME (see the list below)
  -v                 knowledge: show the input and output of every LLM call; training: show every question and answer
  -h, --help         show this help
  (other options are passed to the runner: knowledge: -experiment, -experiment-desc, -models, -reasoning, -atif, -db (go run ./internal/evals/knowledge/cmd -h);
   training: -model, -list-models, -db, -sidecar, -batch-size, -judge-model, -max-tokens, -temperature (go run ./internal/evals/training/cmd -h))

Case names restrict the run to those cases (combined with --eval, they must belong to that eval).

The eval directories are set in $CONFIG (evals_knowledge_dirs, evals_training_dirs; default config bin/gligabao.toml, GLIGABAO_CONFIG
overrides it); -evals DIR (repeatable) overrides them.

Knowledge evals (evals_knowledge_dirs):
USAGE
	go run ./internal/evals/knowledge/cmd -config "$CONFIG" -list-evals
	echo
	echo "Training evals (evals_training_dirs):"
	go run ./internal/evals/training/cmd -config "$CONFIG" -list-evals
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

# Training evals need the sidecar: start it (./run_server_sidecar_local.sh) if nothing answers on its health endpoint, as
# ./run_server_local.sh does. The sidecar started here is stopped when this script exits or is killed (INT/TERM/HUP).
# Not needed (and not started) to list the evals or the models. A -sidecar URL or SIDECAR_URL that is not local is never started.
SIDECAR_PID=""
cleanup() {
	trap - EXIT INT TERM HUP
	if [ -n "$SIDECAR_PID" ]; then
		echo "stopping local sidecar (pid $SIDECAR_PID)" >&2
		kill "$SIDECAR_PID" 2>/dev/null || true
		wait "$SIDECAR_PID" 2>/dev/null || true
	fi
}

start_sidecar() {
	local url="${SIDECAR_URL:-http://${SIDECAR_HOST:-127.0.0.1}:${SIDECAR_PORT:-8787}}" prev="" a
	for a in ${args[@]+"${args[@]}"}; do
		case "$prev" in -sidecar | --sidecar) url="$a" ;; esac
		case "$a" in -sidecar=* | --sidecar=*) url="${a#*=}" ;; esac
		case "$a" in -list-evals | -list-models) return 0 ;; esac
		prev="$a"
	done
	if curl -fs -m 2 "$url/health" >/dev/null 2>&1; then
		echo "sidecar already running at $url" >&2
		return 0
	fi
	case "$url" in
	http://127.0.0.1[:/]* | http://127.0.0.1 | http://localhost[:/]* | http://localhost) ;;
	*)
		echo "warning: sidecar at $url does not answer and is not local; not starting it" >&2
		return 0
		;;
	esac
	echo "sidecar not running at $url; starting it" >&2
	trap cleanup EXIT
	trap 'exit 143' INT TERM HUP
	SIDECAR_HOST="${SIDECAR_HOST:-127.0.0.1}" ./run_server_sidecar_local.sh &
	SIDECAR_PID=$!
	local _
	for _ in $(seq 1 60); do
		curl -fs -m 2 "$url/health" >/dev/null 2>&1 && return 0
		if ! kill -0 "$SIDECAR_PID" 2>/dev/null; then
			echo "warning: sidecar exited at startup (are MODAL_TOKEN_ID / MODAL_TOKEN_SECRET set?); continuing without it" >&2
			SIDECAR_PID=""
			return 0
		fi
		sleep 1
	done
}

case "$kind" in
knowledge) go run ./internal/evals/knowledge/cmd -config "$CONFIG" ${args[@]+"${args[@]}"} ;;
training)
	start_sidecar
	# Not exec'd, so the trap can stop the sidecar. SIDECAR_URL tells the runner where the sidecar is, unless -sidecar does.
	rc=0
	SIDECAR_URL="${SIDECAR_URL:-http://${SIDECAR_HOST:-127.0.0.1}:${SIDECAR_PORT:-8787}}" \
		go run ./internal/evals/training/cmd -config "$CONFIG" ${args[@]+"${args[@]}"} || rc=$?
	exit "$rc"
	;;
*)
	echo "error: unknown eval type \"$kind\" (knowledge or training)" >&2
	exit 2
	;;
esac
