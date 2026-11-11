# Close the gaps left by stage 2 of the deep-research loop

**Date** 2026-11-11 · **Version** 0.181.0 · **Status** open

## Context

Stage 2 (`docs/agents/deep-research-loop-stage-2.md`) connected the investigative loop (`loop = "investigative"`, package `internal/agent_research/investigate`, wiring in `internal/agent_research/investigative.go`). It is tested with a fake model on SQLite; the points below were knowingly left (the scope of the stage, or no real model, server or PostgreSQL at hand).

## Remaining work

- [ ] **Compare the investigative loop with the simple loop on real topics** (stage 2, item 13). Run both on the same topics with a real model and measure the cost, the verified claims (`gb_agent_claims`), the gaps reported, the turns and the stop reasons (the query `runs_by_stop_reason` of `docs/agents/research.md` helps); keep the topics in `build/evals_knowledge/` if they are knowledge evals (each needs a manifest `<name>.md`, see `AGENTS.md`); record the numbers in the changelog or in `docs/agents/deep-research-loop.md`. Do not change the default loop from one run.
- [ ] **Run the loop once on a real model** for a small topic and check the prompt (the instructions in `investigate/prompt.go` are untested against a real model: wording of the extraction, challenge and wrap-up instructions).
- [ ] **Admin UI: render the frontier tree** from `gb_agent_frontier_questions.parent_id` with statuses, the transitions and the challenges of a run (the SQL is in `docs/agents/research.md`, "Reading a run with SQL"); `core.Run` already has `phase` and `stop_reason` to show.
- [ ] **Run the PostgreSQL tests** of stage 2: `TestMigrationAddsPhaseAndStopReason`, `TestFreshDatabaseHasPhaseAndStopReason`, `TestInvestigativeRunThroughTheManager` and the `investigate` loop tests skip without `POSTGRES_TEST_DSN`. Fix what fails (the doc queries use `?1`, the test rewrites it to `$1`; `SUM(outcome = 'survived')` in `challenges_per_target_and_strategy` is SQLite syntax and needs `SUM((outcome = 'survived')::int)` in PostgreSQL).
- [ ] **Parallel workers on the frontier** (`max_parallel` is 1 and 1 is the only accepted value) and sub-agents that share the notes (the investigative loop refuses `[spawn] enabled`).
- [ ] **The `modal` runner for the investigative loop** (refused at start): the agent binary has to carry the loop, the notes directory and the investigate store.
- [ ] **Validate the knobs against each other** when loading a definition (for example `min_domains <= min_sources`, `keep_turns` against `max_iterations`); only each range is checked now.

## How to pick it up

`go test ./internal/agent_research/... ./internal/agent_defs/...`; for PostgreSQL set `POSTGRES_TEST_DSN`. The design is `docs/agents/deep-research-loop.md`, the stage page has an "Implementation notes" section with every place the code chose. Done when the checklist is empty and `./run_tests.sh` passes with PostgreSQL enabled.
