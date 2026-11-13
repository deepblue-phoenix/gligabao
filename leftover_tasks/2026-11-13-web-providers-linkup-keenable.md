# Add Linkup and Keenable as web search and fetch providers

**Date**: 2026-11-13
**Version**: 0.199.0
**Status**: open

## Context

Web search and fetch of the agents go through one wrapper, `internal/sys/web` (`web.Search` and `web.Fetch`, which take a `web.Provider`). Done: `anthropic` for search (Anthropic's cloud web search, via `llm.Client.WebSearch`) and `local` for fetch (plain HTTP GET + crude HTML to text, with the allowed/blocked domain checks, redirects included). `linkup` and `keenable` are declared as providers but return `web.ErrNotImplemented` for both operations; so does `anthropic` for fetch.

## Remaining work

- [x] Linkup fetch done (0.199.0, `linkup.go`, key from `LINKUP_API_KEY`).
- [x] Linkup search done (0.199.0, `linkup.go`).
- [ ] **Linkup settings that the requests do not set** (`internal/sys/web/linkup.go`; today fixed in code or left to Linkup's defaults; make them configurable per agent definition/request/config if wanted):
  - Search `depth`: always `standard`. Also available: `flash` (alpha, <200 ms, no LLM), `fast` (~1 s, one-shot retrieval, no agentic planning) and `deep` (multi-iteration search-and-scrape with evaluation, 5-30 s, $0.05 per call, $0.055 with an answer). A `deep` option is the natural one for the investigative loop.
  - Search `outputType`: always `sourcedAnswer`. Also `searchResults` (`{name, url, content}` only, no LLM, $0.005 instead of $0.006) and `structured` (JSON matching `structuredOutputSchema`).
  - Search `maxResults` (cap on sources), `fromDate` / `toDate` (ISO 8601 date range; could be fed by the `recency` of `[search]` in the agent definitions), `includeImages`, and the premium sources (AFP dispatches, for authorized organizations).
  - Search `includeDomains` / `excludeDomains` are cut to the first 100 domains, silently. The location (`Location`) is not sent: Linkup has no such parameter.
  - The search cost is the flat $0.006 of `standard` + `sourcedAnswer`; it does not change with the settings above, so it has to follow them when they become configurable. The fetch fees are also hardcoded ($0.005), and both are not yet in `internal/sys/config/prices.go`.
  - Fetch `mode`: always `standard`; `pro` has a much higher success rate on hard pages ($0.005 without / $0.01 with `renderJs`).
  - Fetch `renderJs`: always `true` ($0.005; `false` is $0.001 in `standard` and is enough for server-rendered sites).
  - Fetch `includeRawHtml` and `extractImages` (image URLs): not requested, so never returned.
  - Fetch `schema` / `instructions` (structured JSON of the page, +$0.001): not used. `fetch_url` returns only the markdown, cut to `max_page_chars` characters after the download.
  - Fetch limits of Linkup: HTML pages over 20 MB and PDFs over 100 MB give a `400`; other binary URLs (ZIP, image, video) too; no authentication (a login wall returns what an anonymous visitor sees); it does not follow links. Linkup follows redirects itself, so the domain lists of `fetch_url` are checked only on the requested host, not on the final one.
  - The request/response is not retried (no retry on `429` or `5xx`), and the HTTP client of `fetch_url` is used for the Linkup call too, with no timeout beyond the run's context.
- [ ] **Keenable** search and fetch (`keenable.go`), the same way (results only, `Answer` empty).
- [ ] Anthropic fetch (its web fetch tool), if wanted; today `fetch_url` always uses `web.Local`.
- [ ] Domain lists for search: filter the `Results` of providers that do not enforce them.
- [ ] Make `linkup` / `keenable` usable: add them to `implementedSearch` / `implementedFetch` in `internal/sys/web/web.go` (that is what the request, the config and the CLI validate against), and add their API keys in `gligabao.toml` (Linkup's is done: `linkup_api_key` / `LINKUP_API_KEY`; do the same for `KEENABLE_API_KEY`) with an error when the key of the chosen provider is missing.
- [ ] A `provider` field in `[search]` and `[search.fetch]` of the agent definitions (`internal/agent/agent_defs`), if wanted. Today the choice is per request (`search_provider`, `fetch_provider`) and per server (`research_search_provider`, `research_fetch_provider`), done in `agent_research` (`Options`, `Manager.applyDef`) and `agent.WithWebProviders`.
- [ ] The `modal` runner supports only `anthropic` + `local` (`Manager.applyDef` rejects others): pass the providers and keys to the sidecar job (`RemoteRequest`, `sidecar.StartResearchJobRequest`, `gligabao_agent` flags) and lift the check.
- [ ] Store the providers with the run (`gb_agent_research_runs`) and show them in the admin UI's start dialog; today they are not recorded.
- [ ] Prices of the per-call fees in `internal/sys/config/prices.go`, returned as `CostUSD` (so the cost caps work).
- [ ] The system prompt of `web_search` (`prompts.WebSearch`) assumes a model that writes an answer; adjust it for providers that return only results.
- [ ] `remember`'s `web_search` stage (`memory_core.WebSearcher`) and the admin web-search stats (`internal/api/admin/websearch.go`) assume Anthropic's format; record the provider and let the stage choose one.

## How to pick it up

`go test ./internal/sys/web/... ./internal/agent/...`. Each item is independent. Bump `VERSION` and `CHANGELOG.md` as `AGENTS.md` says; delete this file when all is done.
