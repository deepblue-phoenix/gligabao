# Finish the loose ends of the eval run manager

**Date**: 2026-11-21
**Version**: 0.224.0
**Status**: open

## Context
Step 4 of `../gloflow_emergence/docs/evals/eval-refactor.md` added `internal/evals/runmanager` and the owner-aware executor/rerunner. Some
parts were left out because they belong to later steps or are tuning.

## Remaining work
- [ ] Make the concurrency of the eval remember manager configurable (today one goroutine serializes the evals of all users; `runmanager.New`).
- [ ] A rerun is recorded as an eval run with no `runner_id`, so it has no heartbeat: a crash leaves it `running` forever (the stale sweep only
      fails runs with a `runner_id`). Give reruns a runner id and a heartbeat (`knowledge.Rerunner`, `runner.EvalOptions`).
- [ ] Postgres: `runmanager` tests only ran on sqlite here (`POSTGRES_TEST_DSN` unset); run `./run_tests.sh` with it set.

## Progress
- 2026-11-22 (0.224.0, step 5): the commentary cost now counts in `SumCostSince` and the legacy open-by-path code of the rerunner is gone.

## How to pick it up
`go test ./internal/evals/...` (set `POSTGRES_TEST_DSN` for the postgres runs). Done when every box above is ticked or obsolete.
