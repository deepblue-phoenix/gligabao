#!/usr/bin/env bash
# Builds and uploads the two PyPI packages of gligabao. See docs/pypi.md.
#
#   gligabao         clients/py          the Python client (pure Python, one wheel + sdist)
#   gligabao-server  clients/py_server   the server: one wheel per platform, each with the Go binary + gligabao.toml
#
# With no flags it builds the client and the Linux amd64 server (with sargo) and uploads them (after a confirmation):
# the same as --build_client --build_server_linux --arch amd64 --upload.
#
# All logic lives in ops/pypi.py (it runs on Linux, macOS and Windows: `python ops/pypi.py ...`); this script only calls it,
# with the same flags. Run `./run_pypi.sh --help` for them. Examples:
#
#   ./run_pypi.sh --build_server_linux --arch amd64      # one platform, nothing uploaded
#   ./run_pypi.sh --build_client --build_server          # everything, nothing uploaded
#   ./run_pypi.sh --upload --test                        # upload what is built, to TestPyPI
#   ./run_pypi.sh --build_client --build_server --upload
set -euo pipefail
cd "$(dirname "$0")"
exec "${PYTHON:-python3}" ops/pypi.py "$@"
