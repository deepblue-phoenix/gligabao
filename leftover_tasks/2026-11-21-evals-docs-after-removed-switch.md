# Rewrite the eval docs that still describe the removed eval switch and public eval routes

**Date**: 2026-11-21
**Version**: 0.225.0
**Status**: open

## Context
Step 6 of `../gloflow_emergence/docs/evals/eval-refactor.md` removed the `eval` setting, `serve --eval` and `serve --eval-db`, and the public
routes `POST /v1/gb/eval/remember` and `/v1/gb/eval/query` (the knowledge eval runner calls the same handlers through its own private mux).
The code, `build/gligabao.toml`, `run_server_local.sh` and the README paragraph on evals were updated. The eval docs were left to step 9 of the
refactor, because they describe the whole per-user design, not only the switch. Until then they describe a server that no longer exists.

## Remaining work
- `docs/evals/knowledge.md`: the "Eval mode" section (the switch, `POST /v1/gb/eval/remember|query`) becomes the description of the user
  routes and of the private mux of the runner; line ~62 (the eval database `build/gligabao-eval.db`, `serve --eval-db`) becomes the per-user
  evals in the main database; line ~158 (the runner "starts an in-process server with eval mode on") becomes "runs each case through the
  private mux of `knowledge.Caller`, with no listener".
- `docs/evals/experiments.md`: the admin "Evals" block paragraph (line ~78, `--eval-db`) and the rerun paragraph (line ~92, "opens the eval
  database (`--eval-db`)"): the admin API reads the main database, and a rerun is an eval run (see the step 5 notes).
- `docs/hooks.md` (line ~58): the events of the `eval` variants (`POST /v1/gb/eval/...`) are gone.
- `docs/roadmap/ROADMAP.md`: the eval refactor row, once the refactor is done (step 9).
- `../gligabao_docs/` (the Mintlify site): `getting-started/configuration.mdx` (line ~59, `serve --eval`) and `evals/evals.mdx`,
  `evals/experiments.mdx` (`--eval-db`). Do it with the `update gligabao docs` flow, from the updated `docs/`.
- `../gligabao_int_site/` (`evals.html` line ~149 and ~154, `--eval`, the eval database): the same, through the `update gligabao docs` flow.
- `../gligabao_int/docs/config.md`: the evals_* settings are not listed yet (step 8 of the refactor).

## How to pick it up
- Read `docs/evals/` and `../gloflow_emergence/docs/evals/eval-refactor.md` (sections 7, 9 and 10 describe the final behaviour).
- `rg -n 'eval-db|--eval\b|eval = |eval/remember|eval/query|EvalModeEnable' docs ../gligabao_docs ../gligabao_int_site` lists the stale spots.
- Done when that search finds nothing that describes the removed switch or routes (the words `--eval` for `run_evals.sh` stay, they mean something else).
