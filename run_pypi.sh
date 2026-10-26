#!/usr/bin/env bash
# Builds and uploads the two PyPI packages of gligabao. See docs/pypi.md.
#
#   gligabao         clients/py          the Python client (pure Python, one wheel + sdist)
#   gligabao-server  clients/py_server   the server: one wheel per platform, each with the Go binary + gligabao.toml
#
# Building and uploading are independent: building writes wheels to the dist/ directory of each package, --upload
# uploads what is in those directories (the current version of each package only). Combine them in one call to do both.
#
# Usage: ./run_pypi.sh [build flags] [--upload [--test] [--yes]]
#
# Build flags:
#   --build_client            the Python client
#   --build_server            the server, for all platforms (= the three flags below)
#   --build_server_linux      the server for Linux   (amd64, arm64; wheels also tagged musllinux, the binary is static)
#   --build_server_darwin     the server for macOS   (amd64, arm64)
#   --build_server_windows    the server for Windows (amd64, arm64)
#   --arch amd64|arm64        restrict the server builds above to one architecture
#   --no-admin-ui             build the server without the embedded admin UI (default: embedded; needs node + npm)
#
# Upload flags:
#   --upload                  upload every built wheel/sdist of the current versions (build flags may precede it)
#   --test                    upload to TestPyPI instead of PyPI
#   --yes                     do not ask for confirmation
#
# Auth: set TWINE_PASSWORD to your API token (pypi-...); TWINE_USERNAME defaults to __token__. Otherwise twine uses
# ~/.pypirc or prompts.
#
# Examples:
#   ./run_pypi.sh --build_server_linux --arch amd64      # one platform, nothing uploaded
#   ./run_pypi.sh --build_client --build_server          # everything, nothing uploaded
#   ./run_pypi.sh --upload --test                        # upload what is built, to TestPyPI
#   ./run_pypi.sh --build_client --build_server --upload
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$PWD

build_client=0
os_list=()
arch=""
admin_ui=true
upload=0
test_pypi=0
assume_yes=0

usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }

add_os() { os_list+=("$1"); }

while [ $# -gt 0 ]; do
  case "$1" in
    --build_client) build_client=1 ;;
    --build_server) add_os linux; add_os darwin; add_os windows ;;
    --build_server_linux) add_os linux ;;
    --build_server_darwin) add_os darwin ;;
    --build_server_windows) add_os windows ;;
    --arch)
      [ $# -ge 2 ] || { echo "--arch needs a value (amd64 or arm64)" >&2; exit 2; }
      arch="$2"; shift
      case "$arch" in amd64|arm64) ;; *) echo "--arch must be amd64 or arm64" >&2; exit 2 ;; esac ;;
    --no-admin-ui) admin_ui=false ;;
    --upload) upload=1 ;;
    --test) test_pypi=1 ;;
    --yes|-y) assume_yes=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

if [ "$build_client" -eq 0 ] && [ ${#os_list[@]} -eq 0 ] && [ "$upload" -eq 0 ]; then
  echo "nothing to do: give a build flag and/or --upload" >&2
  usage >&2
  exit 2
fi
if [ "$test_pypi" -eq 1 ] && [ "$upload" -eq 0 ]; then
  echo "--test only applies with --upload" >&2
  exit 2
fi
if [ -n "$arch" ] && [ ${#os_list[@]} -eq 0 ]; then
  echo "--arch only applies to the --build_server* flags" >&2
  exit 2
fi

for mod in build twine; do
  python -c "import $mod" 2>/dev/null || python -m pip install --quiet "$mod"
done

CLIENT_DIR="$ROOT/clients/py"
SERVER_DIR="$ROOT/clients/py_server"
STAGE_ROOT="$ROOT/build/pypi"

client_version=$(python - <<'PY'
import tomllib
with open("clients/py/pyproject.toml", "rb") as f:
    print(tomllib.load(f)["project"]["version"])
PY
)
server_version=$(tr -d '[:space:]' < "$ROOT/VERSION")

# Prints "<goos> <goarch> <wheel platform tag>" lines for one OS. The Linux binary is static (CGO_ENABLED=0), so the
# same file serves glibc (manylinux) and musl (musllinux) systems. The macOS minimum is what the Go toolchain supports.
targets_for() {
  case "$1" in
    linux)
      echo "linux amd64 manylinux2014_x86_64.musllinux_1_1_x86_64"
      echo "linux arm64 manylinux2014_aarch64.musllinux_1_1_aarch64" ;;
    darwin)
      echo "darwin amd64 macosx_12_0_x86_64"
      echo "darwin arm64 macosx_12_0_arm64" ;;
    windows)
      echo "windows amd64 win_amd64"
      echo "windows arm64 win_arm64" ;;
  esac
}

#---------------------------------------------------------------------------------------------
# Client

build_client_pkg() {
  echo "==> Building gligabao (client) ${client_version}"
  (
    cd "$CLIENT_DIR"
    PYTHONPATH=src python -m pytest -q
    # Only this version's files are replaced: wheels of other versions stay in dist/.
    mkdir -p dist
    rm -f dist/gligabao-"${client_version}"-*.whl dist/gligabao-"${client_version}".tar.gz
    python -m build --outdir dist
    python -m twine check dist/gligabao-"${client_version}"*
  )
}

#---------------------------------------------------------------------------------------------
# Server

build_admin_ui() {
  if [ "$admin_ui" = true ]; then
    (
      cd admin_ui
      [ -d node_modules ] || npm ci
      npm run build
    )
  else
    # Drop stale artifacts so they are not embedded; keep the .gitkeep placeholder.
    find admin_ui/dist -mindepth 1 -not -name .gitkeep -delete 2>/dev/null || true
  fi
}

# build_server_wheel <goos> <goarch> <platform tag>
build_server_wheel() {
  local goos="$1" goarch="$2" tag="$3"
  local stage="$STAGE_ROOT/server-$goos-$goarch"
  local exe="gligabao"
  [ "$goos" = windows ] && exe="gligabao.exe"

  echo "==> Building gligabao-server ${server_version} for ${goos}/${goarch} (${tag})"
  rm -rf "$stage"
  mkdir -p "$stage/src/gligabao_server/_bin"
  cp -R "$SERVER_DIR/pyproject.toml" "$SERVER_DIR/hatch_build.py" "$SERVER_DIR/README.md" "$stage/"
  cp -R "$SERVER_DIR/src/gligabao_server/." "$stage/src/gligabao_server/"
  find "$stage" -name __pycache__ -type d -prune -exec rm -rf {} +
  printf '__version__ = "%s"\n' "$server_version" > "$stage/src/gligabao_server/_version.py"

  # The sidecar (sidecar/) is not part of the package. The config is the default one the binary finds next to itself.
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags="-s -w" -o "$stage/src/gligabao_server/_bin/$exe" ./cmd/gligabao
  cp bin/gligabao.toml "$stage/src/gligabao_server/_bin/gligabao.toml"

  mkdir -p "$SERVER_DIR/dist"
  # Platform tags are dotted lists, so the wheel of this target is found by its first tag.
  rm -f "$SERVER_DIR/dist/gligabao_server-${server_version}-py3-none-${tag}.whl"
  GLIGABAO_WHEEL_TAG="$tag" python -m build --wheel --outdir "$SERVER_DIR/dist" "$stage"
  python -m twine check "$SERVER_DIR/dist/gligabao_server-${server_version}-py3-none-${tag}.whl"
}

build_server_pkgs() {
  PYTHONPATH="$SERVER_DIR/src" python -m pytest -q "$SERVER_DIR/tests"
  build_admin_ui
  local os line built=0
  for os in "${os_list[@]}"; do
    while read -r line; do
      # shellcheck disable=SC2086
      set -- $line
      if [ -n "$arch" ] && [ "$2" != "$arch" ]; then continue; fi
      build_server_wheel "$1" "$2" "$3"
      built=$((built + 1))
    done < <(targets_for "$os")
  done
  [ "$built" -gt 0 ] || { echo "no server target matched (--arch $arch)" >&2; exit 2; }
}

#---------------------------------------------------------------------------------------------
# Build

if [ "$build_client" -eq 1 ]; then build_client_pkg; fi
if [ ${#os_list[@]} -gt 0 ]; then
  # De-duplicate (--build_server plus --build_server_linux).
  mapfile -t os_list < <(printf '%s\n' "${os_list[@]}" | awk '!seen[$0]++')
  build_server_pkgs
fi

#---------------------------------------------------------------------------------------------
# Upload

if [ "$upload" -eq 1 ]; then
  files=()
  for f in "$CLIENT_DIR"/dist/gligabao-"${client_version}"-*.whl "$CLIENT_DIR"/dist/gligabao-"${client_version}".tar.gz \
           "$SERVER_DIR"/dist/gligabao_server-"${server_version}"-*.whl; do
    [ -e "$f" ] && files+=("$f")
  done
  if [ ${#files[@]} -eq 0 ]; then
    echo "nothing to upload: no files for gligabao ${client_version} / gligabao-server ${server_version} in the dist/ directories; build first" >&2
    exit 1
  fi

  target="PyPI"
  repo_args=()
  if [ "$test_pypi" -eq 1 ]; then target="TestPyPI"; repo_args=(--repository testpypi); fi

  echo "==> To upload to ${target}:"
  printf '    %s\n' "${files[@]#"$ROOT"/}"
  if [ "$assume_yes" -ne 1 ]; then
    read -r -p "Upload ${#files[@]} file(s) to ${target}? [y/N] " answer
    [[ "$answer" =~ ^[Yy]$ ]] || { echo "aborted"; exit 1; }
  fi

  export TWINE_USERNAME="${TWINE_USERNAME:-__token__}"
  python -m twine upload "${repo_args[@]}" "${files[@]}"
  echo "==> Uploaded ${#files[@]} file(s) to ${target}"
fi
