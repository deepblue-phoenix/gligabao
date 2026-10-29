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

## Roadmap

Anything related to the project roadmap (planned features, plans, priorities, status, design notes for future work) lives in
`docs/roadmap/`. The main list of features/plans is `docs/roadmap/ROADMAP.md`; put any supporting or more detailed roadmap
documents in the same directory and link them from `ROADMAP.md`. Do not scatter roadmap items elsewhere (README, other docs, etc.).
Create `docs/roadmap/ROADMAP.md` if it does not exist yet.
