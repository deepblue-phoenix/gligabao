# Add `evals` commands to the gligabao CLI and the Python client

**Date**: 2026-11-22
**Version**: 0.225.1
**Status**: open

## Context
The per-user evals refactor (`../gloflow_emergence/docs/evals/eval-refactor.md`) added the user API `/v1/gb/evals` (see `docs/evals/user-evals.md`). The CLI (`gligabao ...`, `docs/cli.md`) and the Python client (`docs/pypi.md`) were left out to keep the refactor scoped. Experiments have no user endpoint either.

## Remaining work
- [ ] CLI: `gligabao evals list|get|put|delete|run|runs|run-get|cancel|file put|file delete` over the routes of `docs/evals/user-evals.md` (the CLI already has the generic `api` command).
- [ ] Python client: the same calls in the `gligabao` package, with tests.
- [ ] Document them in `docs/cli.md`, `docs/pypi.md`, the Mintlify site and `gligabao_int_site` (`update gligabao docs`).
- [ ] (Later) a user API for experiments, if wanted.

## How to pick it up
Read `docs/cli.md` and `internal/api/evals/register.go`. Done when the commands work against a local daemon and the docs list them.
