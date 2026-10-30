#!/usr/bin/env bash
# Builds rust/sargo in release mode (output: rust/sargo/target/release/sargo). Needs cargo.
set -euo pipefail
cd "$(dirname "$0")/rust/sargo"

cargo build --release
