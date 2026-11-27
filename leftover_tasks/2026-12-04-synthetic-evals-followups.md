# Finish the follow-ups of the synthetic evals

**Date**: 2026-12-04
**Version**: 0.259.0
**Status**: open

## Context

0.259.0 added synthetic evals (`docs/evals/synthetic-evals.md`, `internal/evals/synth`, routes under `/v1/gb/evals/synth`, tables `gb_synth_*`). It was kept to the user API, the schema and the evals modal of gloflow_emergence; these related parts were left out.

## Remaining work

- [ ] The cost of a generation (`gb_synth_generations.cost_usd`) is not counted in the per-user daily cap: `SumCostSince` (`internal/evals/db/reads_user.go`) sums runs, reruns and commentaries only. Add the generations (and keep `Manager.CheckBudget` consistent).
- [ ] The eval list counts the case defs of the database, so after a regeneration that shrinks `num_examples` the orphan case defs (kept by `runner.UpsertEvalDef`) are still counted in `cases`. Count the cases of the file for synthetic evals, or remove the orphans that have no runs.
- [ ] The synthetic evals have no admin API view and no admin UI (the admin list shows `origin` only, through the shared `EvalViews`).
- [ ] The Postgres backend is covered only by the shared SQL (the `dbtest` Postgres variants of `internal/api/evals/t_synth_test.go` are skipped without a Postgres): run them against a real Postgres.
- [ ] The CLI and the Python client (see `2026-11-22-evals-cli-and-python-client.md`) have no commands for the synthetic evals.
- [ ] `docs/` of `gligabao_docs` and the `gligabao_int_site` pages are not updated (done by the `update gligabao docs` flow).

## How to pick it up

Run `go test ./internal/evals/... ./internal/api/evals/`; with Postgres, see how `dbtest.Run` finds it. Done when the items above are checked off and the docs say so.
