# Finish the loose ends of the evals API

**Date**: 2026-11-22
**Version**: 0.224.0
**Status**: open

## Context
Step 5 of `../gloflow_emergence/docs/evals/eval-refactor.md` added `internal/api/evals` (user routes, shared views and handlers), the admin
wrappers on the main database, the `server.Options.EvalManager` hook (plugged in by `evals_manager.go`) and the admin UI update. The
settings and the old flags were left to step 6, and the docs to step 9.

## Remaining work
- [ ] The server builds the files store of the users from `userfiles.Default` or the directory `evals_user` next to the database
      (`internal/sys/server/server.go`, where `opts.EvalManager` is called); the S3 settings (`evals_s3_*`) and the limits (`evals_max_*`) come
      with step 6: pass them through `server.Options` and `runmanager.Config` (`evals_manager.go`).
- [ ] No test mounts the whole server with the `EvalManager` hook (`/v1/gb/evals` through `server.Serve`/`newMux`): add one.
- [ ] The API tests (`internal/api/evals`) and `readstest` only ran on sqlite here (`POSTGRES_TEST_DSN` unset): run them with it set.
- [ ] The admin evals list has no cost per user and the admin UI has no eval-run cancel/delete: add when needed.
- [ ] Docs of the routes (`docs/evals/*.md`, `docs/admin-extensions.md`, `docs/listings`): step 9 of the refactor.

## How to pick it up
`go test ./internal/api/... ./internal/evals/... ./internal/sys/server/...` (set `POSTGRES_TEST_DSN` for the postgres runs). Done when every box
above is ticked or obsolete.
