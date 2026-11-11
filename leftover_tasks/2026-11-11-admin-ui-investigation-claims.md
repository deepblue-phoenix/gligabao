# Show claims, sources and question history of an investigative run in the admin UI

**Date**: 2026-11-11
**Version**: 0.185.0
**Status**: open

## Context
0.185.0 added the Investigation block to the research Details modal (`admin_ui/src/research.ts`, `renderInvestigation`), fed by `ResearchInvestigationOf` (`internal/api/admin/research.go`), which reads `investigate.Store.Load`: frontier, challenges, latest notes. `Load` does not read claims, sources, the question transitions or the notes versions, so they were left out to keep the task small.

## Remaining work
- [ ] Add read methods for `gb_agent_claims` (+ `gb_agent_claim_sources`), `gb_agent_sources`, `gb_agent_frontier_transitions` and `gb_agent_notes_versions` to `investigate.Store`, in both `db/sqlite/investigate_store.go` and `db/postgres/investigate_store.go`.
- [ ] Return them from `ResearchInvestigationOf` and show them in `renderInvestigation` (claims with status and sources, a sources list, a transition history per question, a notes version picker).
- [ ] Update the Admin UI section of `docs/agents/research.md` and the changelog.

## How to pick it up
Follow the SQL in `docs/agents/research.md` ("Reading a run with SQL"). Tests: `go test ./internal/api/admin/ ./internal/agent_research/...`; `cd admin_ui && npm run typecheck`. Done when the modal of a finished investigative run shows claims and sources.
