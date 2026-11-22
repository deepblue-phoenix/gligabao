#!/usr/bin/env bash
# Builds rust/sargo in release mode (output: rust/sargo/target/release/sargo). Needs cargo.
# The build logic lives in ops/build.py (`python3 ops/build.py sargo --help`); this script only calls it.
# Run with --help for the options.
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: ./run_build_rust.sh [--static] [--embed] [-h|--help]

Builds rust/sargo in release mode (for this machine: Linux, macOS or Windows).

  (no flags)   normal build, dynamically linked against the system libc.
               Output: rust/sargo/target/release/sargo
  --static     statically linked build. Linux: musl, runs on any Linux of the same CPU architecture
               (needs `sudo apt install -y musl-tools` on Ubuntu). macOS: target <arch>-apple-darwin.
               Windows: MSVC with a static C runtime.
               Output: rust/sargo/target/<rust target triple>/release/sargo
  --embed      implies --static, then copies the binary to rust/sargo/bin/sargo, where rust/sargo/embed.go embeds it
               into the gligabao binary the next time that is built (run_build.sh --sargo does both steps).
  -h, --help   show this help and exit

Other OS/arch (macOS, Windows, cross-arch): python3 ops/build.py sargo --os ... --arch ... --static
USAGE
}

args=()
for arg in "$@"; do
  case "$arg" in
    -h|--help) usage; exit 0 ;;
    --static|--embed) args+=("$arg") ;;
    *) echo "unknown option: $arg" >&2; usage >&2; exit 2 ;;
  esac
done

cd "$(dirname "$0")"
exec "${PYTHON:-python3}" ops/build.py sargo ${args[@]+"${args[@]}"}
