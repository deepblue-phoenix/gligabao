# Instructions for coding agents / LLMs

## Versioning and changelog (required for every change)

This repo is versioned on its own (separately from `../gligabao_int`). The version lives in two files at the repo root
(`VERSION` is embedded into the binary at build time, see `gligabao.go`):

- `VERSION`: the current semantic version, one line, no `v` prefix (e.g. `0.2.0`).
- `CHANGELOG.md`: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format, newest entry first.

With every change that is committed (code, config defaults, API, schema, build; not pure typo fixes in comments):

1. Pick the new version (semver; while `0.x`): **minor** for new features, behaviour changes or anything breaking
   (API, DB schema, config, CLI flags); **patch** for bug fixes, refactors, docs and tests. Use `1.0.0` only when the user says so.
2. Write the new `VERSION`.
3. Add a new section at the top of `CHANGELOG.md`, directly under the intro: `## [x.y.z] - YYYY-MM-DD` with
   `### Added` / `### Changed` / `### Fixed` / `### Removed` subsections as needed. Describe what changed for a
   user/operator in plain words (not the commit message). Never edit or reorder older entries.
4. Run `./run_tests.sh`: `TestVersionMatchesChangelog` fails if `VERSION` is not semver or is not the newest changelog entry.

If several changes land in one working session before a commit, use one version bump for the whole commit.
Do not add an `[Unreleased]` section: the top entry must always match `VERSION`.

A built binary reports the version via `gligabao --version`, `gligabao version` and `GET /version`.

## Naming of errors

A variable, argument or struct field of type `error` is named `err` or ends with `Err` (`parseErr`, `closeErr`, `causeErr`),
never `e`, `perr`, `err1`, `boom`, etc. Package-level sentinel errors keep the Go prefix (`ErrNotFound`, `errBadName`).

## Manifest of every knowledge eval

Every eval file in `build/evals_knowledge/` (`<name>.json`) must have a manifest next to it, `build/evals_knowledge/<name>.md`
(the loaders only read `*.json`, so the manifest is ignored by them). The manifest explains how the eval was generated, so that more
generations (new cases, a variant, a regeneration) are as reproducible as possible. Create it with every new synthetic eval, and update
it when the eval changes (new cases, new rules). Use `build/evals_knowledge/temporal.md` as the template.

It has these headers, in this order:

- `# Manifest: <name>`, then the standard fields as a list: **Name** (the eval and its file), **Creation date** (`YYYY-MM-DD`, with the commit if known), **Summary** (what the eval covers, in a few lines).
- `## Eval dataset generation skill` (the main section): the goal, how it was generated (who/what generated it, the prompts or process, the rules every case follows, naming), the format, how to add or regenerate cases, and the known limits.

Only `temporal` has a manifest for now; add the others when they are next touched (do not backfill them without being asked).

## Roadmap

Anything related to the project roadmap (planned features, plans, priorities, status, design notes for future work) lives in
`docs/roadmap/`. The main list of features/plans is `docs/roadmap/ROADMAP.md`; put any supporting or more detailed roadmap
documents in the same directory and link them from `ROADMAP.md`. Do not scatter roadmap items elsewhere (README, other docs, etc.).
Create `docs/roadmap/ROADMAP.md` if it does not exist yet.

## Database schema files

The database schema is SQL, not Go strings: one `schema.sql` per backend package (`sqlite/` and `postgres/` of `internal/db/relational`, `internal/db/graph`, `internal/training/db`, `internal/evals/db`, `internal/training_audits/db`, `internal/agent_research/db`), embedded with `go:embed` (the Go file next to it only runs it). Rules:

- `schema*.sql` is the **final state** of the tables for a new database (idempotent: `CREATE ... IF NOT EXISTS`, in dependency order); other tools read these files (e.g. the schema diagram tool of `../gligabao_int_site/tools`), so a new table goes into them, not into Go code. Changes needed for an older database go into `upgrade_*.sql` (or the `Upgrade` of the package) and **also** into `schema*.sql`.
- `optional_*.sql` is DDL that is not applied on every start (the geo index).
- Write plain SQL (no Go `Sprintf` placeholders or string splicing, except the one documented in `optional_geo_index.sql` of PostgreSQL); the SQLite and PostgreSQL files of a feature define the same tables, the SQLite one holding the comments that say what the columns mean.
- Keep `docs/listings/db-tables.md` up to date with every table change.
