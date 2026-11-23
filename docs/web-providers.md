# Web search and fetch providers

The research agents' two web tools, `web_search` and `fetch_url` (called `fetch_page` in the [investigative loop](agents/deep-research-loop.md)), do not call a service directly: they go through one wrapper, the package `internal/sys/web`, which takes the **provider** that does the work. Search and fetch have a provider each, chosen independently.

Code: `internal/sys/web/` (`web.go`, `anthropic.go`, `local.go`, `linkup.go`, `keenable.go`), the tools in `internal/agent/research.go` (`webSearchTool`, `fetchURLTool`), the choice in `internal/agent_research/` (`Options`, `Manager`).

## Providers

| Provider | Search | Fetch | Status |
|---|---|---|---|
| `anthropic` | yes (default) | no | Anthropic's cloud web search |
| `local` | no | yes (default) | a plain HTTP GET from the server |
| `linkup` | yes | yes | Linkup's `/v1/search` and `/v1/fetch` |
| `keenable` | yes | yes | Keenable's `/v1/search` and `/v1/fetch` (keyless by default) |

A provider that is named but cannot do the operation (`anthropic` for fetch, `local` for search) is rejected as soon as it is chosen, not when the agent first uses the tool.

- **`anthropic` (search).** A *server tool*: gligabao sends one Messages API request (model, system prompt, the query) and Anthropic runs the searches in its cloud; what comes back is the model's written answer with its sources. It needs the Anthropic API key and a model that supports the tool. It costs the tokens plus a fee per search (`llm.WebSearchRequestUSD`).
- **`local` (fetch).** gligabao itself downloads the page (at most 1 MiB, up to 10 redirects) and turns the HTML into text (scripts, styles and tags removed, spaces collapsed; crude on purpose), cut to the page limit (`max_page_chars` of the [definition](agents/agent-defs.md), default 15000). It needs no key and costs nothing.
- **`keenable` (search).** `POST {base}/v1/search` (base `https://api.keenable.ai`) with the default mode (`pro`), the results with their text (`snippet`, capped at 2000 characters) and the publish date as `PageAge`. The answer is empty: the model writes it from the results. Cards are disabled (`disable_cards`), so every cited URL is an ordinary page. Without a key the call goes to `/v1/search/public` with the header `X-Keenable-Title: gligabao` (no credits, rate limited per IP address); with a key (`web.Deps.KeenableKey`, `X-Api-Key`) to `/v1/search`, which bills the call. `allowed_domains` become the `site` parameter (one domain) or `(site:a OR site:b)` in the query (several); `blocked_domains` become `exclude_domains` (more than 1000 is an error, not a cut). The location is ignored. The cost (`CostUSD`) is the credits of a paid call at the list price ($0.004 per credit); the free allowance and keyless calls cost nothing. The key is not read from the config yet, so the calls are keyless.
- **`keenable` (fetch).** `GET {base}/v1/fetch?url=...&max_chars=N` (or `/v1/fetch/public` without a key, with the header `X-Keenable-Title: gligabao`): the page as markdown, navigation and boilerplate stripped, cut to the page limit. The default is Keenable's indexed copy; `live` and `prompt` are not used (no live fetch, no extraction). The `url` of the response (after redirects) is the one returned. The domain lists are checked on the requested host before the call and on the final host after it, so text of a blocked site is never returned. The cost is the same as for search: the credits of a paid call at $0.004 each, zero for keyless calls and the free allowance.
- **`linkup` (search).** `POST {base}/v1/search` with `depth` standard (one agentic iteration) and `outputType` sourcedAnswer: the answer with inline citations, and the sources (`name`, `url`, `snippet`, mapped to `Title`, `URL`, `Snippet`). `AllowedDomains` / `BlockedDomains` become `includeDomains` / `excludeDomains` (first 100 each); the location is ignored. Key: `linkup_api_key` / `LINKUP_API_KEY` of the config (`Deps.LinkupKey`). It costs $0.006 per call (`CostUSD`, no tokens).
- **`linkup` (fetch).** `POST {base}/v1/fetch` with `mode` standard and `renderJs` true; the markdown of the page (HTML or PDF) is cut to the page limit. The key is `linkup_api_key` / `LINKUP_API_KEY` of the config (`Deps.LinkupKey`, `agent.WithLinkupKey`); choosing `linkup` without a key is refused (config validation, research start). It costs $0.005 per call (`CostUSD`). Linkup follows redirects itself, so the domain lists are checked on the requested host only.

## Choosing the providers

| Where | Search | Fetch |
|---|---|---|
| Request (`POST /v1/gb/research`) | `search_provider` | `fetch_provider` |
| CLI | `gligabao research start --search-provider P` | `--fetch-provider P` |
| `gligabao_agent` | `--search-provider P` | `--fetch-provider P` |
| MCP `research_start` | `search_provider` | `fetch_provider` |
| Server config (`gligabao.toml`) | `research_search_provider` | `research_fetch_provider` |

The value used is the first that is set: the request, then the server config, then the built-in default (`anthropic` for search, `local` for fetch). The sub-agents (workers) of a research use the choice of their root. Case and spaces do not matter.

```toml
# gligabao.toml: defaults for requests that name no provider
research_search_provider = "anthropic"
research_fetch_provider = "local"
```

```
{"topic": "...", "search_provider": "anthropic", "fetch_provider": "local"}
```

Errors: a name that is not a provider, or one that is not available yet, is a `400` from the API (`search_provider: ...`, `fetch_provider: ...`), a usage error in the CLI, and a startup error when it is in `gligabao.toml`.

Limits of the choice today:

- The `modal` runner supports only `anthropic` and `local` (a `400` otherwise, also when the server's default is something else): the ML sidecar job does not carry the choice yet.
- The choice is not stored with the run (not in `gb_agent_research_runs`, not shown in the admin UI).
- It is per research, not per [agent definition](agents/agent-defs.md): there is no `provider` key in `[search]` / `[search.fetch]`.

## What the tools do with the settings

The domain lists, location and call limits of the definition's `[search]` and `[search.fetch]` tables ([agent-defs.md](agents/agent-defs.md#search-web-preferences)) apply whichever provider runs:

- `max_calls` (search and fetch) is counted by the tool, before the provider is called; a call that is over the limit does not reach it.
- `allowed_domains` / `blocked_domains` of **fetch** are checked by the wrapper before any provider is called, and again at every redirect: a blocked host is an error that says so, and a host outside a non-empty allowed list too.
- `allowed_domains` / `blocked_domains`, `location` and `max_uses_per_call` of **search** are sent to the provider (for `anthropic`: the tool's `allowed_domains`, `blocked_domains`, `user_location` and `max_uses`). The preferred domains, recency, language and instructions are not enforced: they are in the prompt.
- The cost of a search (`CostUSD`, plus its tokens) is reported to the agent's budget (`ReportUsage`), so the [cost caps](agents/agent-defs.md#limits-cost-caps) count it.

## The wrapper

```go
resp, err := web.Search(ctx, web.Anthropic, web.Deps{LLM: client}, web.SearchRequest{
    Query: "...", AllowedDomains: ..., Location: ..., System: ..., MaxUses: 2,
})
page, err := web.Fetch(ctx, web.Local, web.Deps{HTTP: httpClient}, web.FetchRequest{
    URL: "https://...", MaxChars: 15000, BlockedDomains: ...,
})
```

`SearchResponse` is the same whichever provider ran: `Answer` (a written summary; empty for a provider that only returns results), `Results` (`URL`, `Title`, `PageAge`, `Snippet`), `Queries`, `Searches`, the tokens and `CostUSD`. `FetchResponse` has `URL`, `Text` and `CostUSD`. `web.ParseSearch` / `web.ParseFetch` read and validate a provider name (empty gives a default); `ErrNotImplemented` and `ErrUnknownProvider` are returned by `Search` and `Fetch`. `Deps` holds what a provider needs (the Anthropic client, the HTTP client, the Linkup key and base URL).

To make a provider available, implement it in this package and add it to `implementedSearch` / `implementedFetch`: the request, the config and the CLI validate against those lists. The open work for Keenable (the key in the config, the search knobs) is in `leftover_tasks/2026-11-13-web-providers-linkup-keenable.md`.

## Not covered

`remember` with `web_search: true` ([web-search.md](web-search.md)) does not use this wrapper yet: it calls Anthropic's search directly.
