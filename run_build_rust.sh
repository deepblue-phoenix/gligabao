#!/usr/bin/env bash
# Builds rust/sargo in release mode (output: rust/sargo/target/release/sargo). Needs cargo.
set -euo pipefail
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

cargo build --release
