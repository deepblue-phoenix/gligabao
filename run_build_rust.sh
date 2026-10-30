#!/usr/bin/env bash
# Builds rust/sargo in release mode (output: rust/sargo/target/release/sargo). Needs cargo.
# Run with --help for the options.
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: ./run_build_rust.sh [--static] [-h|--help]

Builds rust/sargo in release mode.

  (no flags)   normal build, dynamically linked against the system libc.
               Output: rust/sargo/target/release/sargo
  --static     fully static binary (musl libc, all libraries embedded), which runs on any
               Linux of the same CPU architecture without installed libs.
               Output: rust/sargo/target/<arch>-unknown-linux-musl/release/sargo
               Needs the musl target (added with rustup if available) and, on Ubuntu,
               the musl C compiler: sudo apt install -y musl-tools
  -h, --help   show this help and exit
USAGE
}

STATIC=0
for arg in "$@"; do
  case "$arg" in
    -h|--help) usage; exit 0 ;;
    --static) STATIC=1 ;;
    *) echo "unknown option: $arg" >&2; usage >&2; exit 2 ;;
  esac
done

cd "$(dirname "$0")/rust/sargo"

# rustup installs cargo in ~/.cargo/bin; a fresh shell may not have it on PATH yet.
if ! command -v cargo >/dev/null 2>&1 && [ -x "$HOME/.cargo/bin/cargo" ]; then
  export PATH="$HOME/.cargo/bin:$PATH"
fi

if ! command -v cargo >/dev/null 2>&1; then
  cat >&2 <<'MSG'
cargo (Rust) is not installed. To install it on Ubuntu:

  1. Install a C linker/compiler, which Rust needs to link binaries:
       sudo apt update && sudo apt install -y build-essential curl

  2. Install Rust with rustup (the official installer; choose the default install when asked):
       curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh

  3. Load cargo into the current shell (new shells get it automatically):
       . "$HOME/.cargo/env"

  4. Check it works, then run this script again:
       cargo --version

MSG
  exit 1
fi

if [ "$STATIC" = 1 ]; then
  TARGET="$(uname -m)-unknown-linux-musl"
  if command -v rustup >/dev/null 2>&1; then
    rustup target add "$TARGET"
  fi
  if command -v musl-gcc >/dev/null 2>&1; then
    # The TLS dependency (ring) compiles C code; cc-rs looks for <arch>-linux-musl-gcc, but Ubuntu's musl-tools only ships musl-gcc.
    export "CC_$(echo "$TARGET" | tr - _)=musl-gcc"
  else
    echo "error: musl-gcc not found (needed to compile C code of dependencies for the musl target). On Ubuntu: sudo apt install -y musl-tools" >&2
    exit 1
  fi
  cargo build --release --target "$TARGET"
  echo "static binary: rust/sargo/target/$TARGET/release/sargo"
else
  cargo build --release
fi
