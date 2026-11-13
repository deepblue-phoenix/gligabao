# Add Linkup and Keenable as web search and fetch providers

**Date**: 2026-11-13
**Version**: 0.197.0
**Status**: open

## Context

Web search and fetch of the agents go through one wrapper, `internal/sys/web` (`web.Search` and `web.Fetch`, which take a `web.Provider`). Done: `anthropic` for search (Anthropic's cloud web search, via `llm.Client.WebSearch`) and `local` for fetch (plain HTTP GET + crude HTML to text, with the allowed/blocked domain checks, redirects included). `linkup` and `keenable` are declared as providers but return `web.ErrNotImplemented` for both operations; so does `anthropic` for fetch.

## Remaining work

- [ ] **Linkup** search and fetch in `internal/sys/web` (`linkup.go`): plain HTTP APIs, bearer key; map `AllowedDomains`/`BlockedDomains` to its include/exclude domains; fill `Answer` (if its sourced answer is used) and `Results` (with `Snippet`); `httptest` tests.
- [ ] **Keenable** search and fetch (`keenable.go`), the same way (results only, `Answer` empty).
- [ ] Anthropic fetch (its web fetch tool), if wanted; today `fetch_url` always uses `web.Local`.
- [ ] Domain lists for search: filter the `Results` of providers that do not enforce them.
- [ ] Make `linkup` / `keenable` usable: add them to `implementedSearch` / `implementedFetch` in `internal/sys/web/web.go` (that is what the request, the config and the CLI validate against), and add their API keys (`LINKUP_API_KEY`, `KEENABLE_API_KEY`, `gligabao.toml`) with an error when the key of the chosen provider is missing.
- [ ] A `provider` field in `[search]` and `[search.fetch]` of the agent definitions (`internal/agent/agent_defs`), if wanted. Today the choice is per request (`search_provider`, `fetch_provider`) and per server (`research_search_provider`, `research_fetch_provider`), done in `agent_research` (`Options`, `Manager.applyDef`) and `agent.WithWebProviders`.
- [ ] The `modal` runner supports only `anthropic` + `local` (`Manager.applyDef` rejects others): pass the providers and keys to the sidecar job (`RemoteRequest`, `sidecar.StartResearchJobRequest`, `gligabao_agent` flags) and lift the check.
- [ ] Store the providers with the run (`gb_agent_research_runs`) and show them in the admin UI's start dialog; today they are not recorded.
- [ ] Prices of the per-call fees in `internal/sys/config/prices.go`, returned as `CostUSD` (so the cost caps work).
- [ ] The system prompt of `web_search` (`prompts.WebSearch`) assumes a model that writes an answer; adjust it for providers that return only results.
- [ ] `remember`'s `web_search` stage (`memory_core.WebSearcher`) and the admin web-search stats (`internal/api/admin/websearch.go`) assume Anthropic's format; record the provider and let the stage choose one.

## How to pick it up

`go test ./internal/sys/web/... ./internal/agent/...`. Each item is independent. Bump `VERSION` and `CHANGELOG.md` as `AGENTS.md` says; delete this file when all is done.
