# Finish the loose ends of the eval run manager

**Date**: 2026-11-21
**Version**: 0.223.0
**Status**: open

## Context
Step 4 of `../gloflow_emergence/docs/evals/eval-refactor.md` added `internal/evals/runmanager` and the owner-aware executor/rerunner. Some
parts were left out because they belong to later steps or are tuning.

## Remaining work
- [ ] Make the concurrency of the eval remember manager configurable (today one goroutine serializes the evals of all users; `runmanager.New`).
- [ ] A rerun is recorded as an eval run with no `runner_id`, so it has no heartbeat: a crash leaves it `running` forever (the stale sweep only
      fails runs with a `runner_id`). Give reruns a runner id and a heartbeat (`knowledge.Rerunner`, `runner.EvalOptions`).
- [ ] The cost of a commentary (`internal/api/admin/evals_commentary.go`, step 5 moves it to `internal/api/evals`) is not part of `SumCostSince`
      (it only sums eval runs): count it in the daily cap.
- [ ] The separate eval database (`EvalRerunEnv.DBPath`, `Rerunner.database`) disappears with step 5/6: delete the legacy open-by-path code.
- [ ] Postgres: `runmanager` tests only ran on sqlite here (`POSTGRES_TEST_DSN` unset); run `./run_tests.sh` with it set.

## How to pick it up
`go test ./internal/evals/...` (set `POSTGRES_TEST_DSN` for the postgres runs). Done when every box above is ticked or obsolete.
