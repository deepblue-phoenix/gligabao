# Finish the loose ends of the run_code sandbox (Monty)

**Date**: 2026-11-12
**Version**: 0.187.0
**Status**: open

## Context

0.187.0 added `run_code` for investigative agents (`docs/agents/monty-code-execution.md`, section "As built"). The core is done: `internal/agent/code_sandbox/hostfn`, `internal/agent/code_sandbox` (bridge and sidecar), the `[code]` table, the tool in `internal/agent_research/investigate/code.go`, the Dockerfile. The parts below were left out to keep the task scoped.

## Remaining work

- [ ] CLI flags for the settings (`serve` and `gligabao_agent`: `--monty-python`, `--monty-bin`, `--monty-workers`); today only `gligabao.toml` and the env variables.
- [ ] Check the sandbox when the definitions load (a `[code] enabled` definition on a server without Monty is an error at boot, as the plan says); today the check is at the start of each run (`researcher.codeConfig` in `internal/agent_research/investigative.go`). Also print whether the sandbox is available in the startup log.
- [ ] Docker build check that `/opt/monty/bin/monty -c "print(1+1)"` runs as user `gligabao` (the Dockerfile change was not built in this task).
- [ ] A `max_host_calls` budget per run (today per `run_code` call), and the wall time of `run_code` against a run time limit if the definition has one.
- [ ] A loop-level test of the cancel path with real Monty (`Manager.Stop` while a program runs), and of a worker that ignores the limits (the `monty_request_timeout_s` backstop).
- [ ] The items of "Leftovers" in `docs/agents/monty-code-execution.md` (Go speaking the Monty protocol directly, state between calls, code in synthesis and sub-agents, mutating host functions, admin UI view of code calls).

## How to pick it up

`go test ./internal/agent/code_sandbox/... ./internal/agent_research/... ./internal/agent/agent_defs/...` (the integration tests of `internal/agent/code_sandbox` skip when `pydantic_monty` is not installed for `GLIGABAO_MONTY_PYTHON` / `python3`). Each item is independent. Bump `VERSION` and `CHANGELOG.md` as `AGENTS.md` says; delete this file when all is done.
