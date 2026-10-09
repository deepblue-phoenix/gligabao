#!/usr/bin/env bash
# Source this to build/test with CGO (needed for the sqlite-vec vector store).
#
# sqlite-vec's Go bindings #include "sqlite3.h", which mattn/go-sqlite3 ships as
# sqlite3-binding.h. We expose it under the expected name via a symlink and put it on the C include path.
# Requires a C compiler (gcc/clang).
_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_mattn="$(cd "$_root" && go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3)"
mkdir -p "$_root/.cgo-include"
ln -sf "$_mattn/sqlite3-binding.h" "$_root/.cgo-include/sqlite3.h"
export CGO_ENABLED=1
export CGO_CFLAGS="${CGO_CFLAGS:-} -I$_root/.cgo-include"
unset _root _mattn
