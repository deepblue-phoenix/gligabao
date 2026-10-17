#!/usr/bin/env bash
# Build and upload clients/py to PyPI as "gligabao".
#
# Usage:
#   ./run_pypi_upload.sh             # upload to PyPI
#   ./run_pypi_upload.sh --test      # upload to TestPyPI
#   ./run_pypi_upload.sh --dry-run   # build + check only, no upload
#
# Auth: set TWINE_PASSWORD to your API token (pypi-...); TWINE_USERNAME defaults
# to __token__. Otherwise twine uses ~/.pypirc or prompts.
set -euo pipefail
cd "$(dirname "$0")/clients/py"

repo_args=()
dry_run=0
for arg in "$@"; do
  case "$arg" in
    --test) repo_args=(--repository testpypi) ;;
    --dry-run) dry_run=1 ;;
    -h|--help) sed -n '2,11p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 1 ;;
  esac
done

for mod in build twine; do
  python -c "import $mod" 2>/dev/null || python -m pip install --quiet "$mod"
done

version=$(python - <<'PY'
import tomllib
with open("pyproject.toml", "rb") as f:
    print(tomllib.load(f)["project"]["version"])
PY
)
echo "==> Building gligabao ${version}"

PYTHONPATH=src python -m pytest -q
rm -rf dist
python -m build
python -m twine check dist/*

if [ "$dry_run" -eq 1 ]; then
  echo "==> Dry run: skipping upload"
  exit 0
fi

export TWINE_USERNAME="${TWINE_USERNAME:-__token__}"
target="PyPI"
[ ${#repo_args[@]} -gt 0 ] && target="TestPyPI"
read -r -p "Upload gligabao ${version} to ${target}? [y/N] " answer
[[ "$answer" =~ ^[Yy]$ ]] || { echo "aborted"; exit 1; }

python -m twine upload "${repo_args[@]}" dist/*
echo "==> Uploaded gligabao ${version} to ${target}"
