# Close the gaps left by stage 1 of the deep-research loop

**Date** 2026-11-11 · **Version** 0.180.0 · **Status** open (stage 2, 0.181.0, closed the four items about the loop; the PostgreSQL run, the persisted columns and semantic dedup remain)

## Context

Stage 1 (`docs/agents/deep-research-loop-stage-1.md`, package `internal/agent_research/investigate`) added the state values, tools, tables, store and notes file. The investigative loop is stage 2 and is not connected: no agent uses `InvestigativeRegistry`. The points below were knowingly left.

## Remaining work

- [ ] **Run the PostgreSQL tests.** `db/postgres/investigate_store.go` was derived from the SQLite one and the PostgreSQL migration test (`migration_test.go`) and store tests (`investigate/store_test.go`) skip without `POSTGRES_TEST_DSN`; no server was available when this was written. Run them against a PostgreSQL and fix what fails.
- [ ] **Persist what has no column.** The independence declaration of a claim-source link, `check` and `note` of a claim live only in the notes text (the latest `gb_agent_notes_versions` row is the authority when a run is loaded). If the claim tables should be authoritative, add columns (`independent` on `gb_agent_claim_sources`, `check_hint`, `note` on `gb_agent_claims`) by `ALTER` in both backends and load from them.
- [x] **Wire the search tools to the attempt log** (stage 2): `web_search`, `fetch_page` and the challenge search call `investigate.LinkAttempt` / insert attempts. Until then only `retry_question` and the tests write attempts.
- [x] **Connect `research_notes_dir`**: build `NotesFile{Dir: cfg.ResearchNotesDirOrDefault()}` where the manager starts a run (stage 2); `notes_id` is already made by `CreateRun`. Runs started in Modal (`remote.go`) get a `notes_id` but no notes.
- [x] **Restore the notes file from the latest version** when it is missing at the start of a turn (`reason = 'restore'`), and the consolidation turn (`reason = 'consolidation'`) (stage 2).
- [x] **Exploration and finalization gates, breakers, synthesis loop** (stage 2). `ConclusionSurvived`, `OpenChallenges`, `ChallengeSummary`, `Summary` and `TokenEstimate` are the pure pieces they use.
- [ ] **Semantic duplicate detection** of questions: only normalized string equality is used (as the design's first implementation).

## How to pick it up

`go test ./internal/agent_research/...`; for PostgreSQL set `POSTGRES_TEST_DSN` (see `internal/db/relational/dbtest`). Stage 2 is `docs/agents/deep-research-loop-stage-2.md`. Done when the checklist is empty and `./run_tests.sh` passes with PostgreSQL enabled.
