# Give the Runtime to the admin extension handlers

**Date**: 2026-11-24
**Version**: 0.244.0
**Status**: open

## Context

0.244.0 added `gligabao.Runtime` (package `gbruntime`) and made every callback hook take it (see `hooks_runtime.go`, `monitoring/`). `AdminExtension.Handler` (`internal/api/admin`, `ServeOptions.AdminExtensions`) is a plain `http.Handler`, so it cannot take an extra argument, and was left unchanged to stay scoped.

## Remaining work

- [ ] Decide how an extension backend reaches the Runtime: a `HandlerFunc func(rt *Runtime) http.Handler` field built once at startup (preferred), or the runtime in the request context (`gbruntime.FromContext`).
- [ ] Implement it in `internal/api/admin` (`Extension`, `ResolveExtensions`) and bind it in `hooks_runtime.go`; keep or drop the old `Handler` field (breaking is fine).
- [ ] Update `docs/admin-extensions.md`, `docs/hooks.md` (the sentence that says the handler does not get it), the CHANGELOG, and `../gligabao_int/admin_ext`.

## How to pick it up

`go test ./...` in `gligabao/`. Done when an extension handler can use `rt.Relational` and a test in `t_runtime_test.go` shows it.
