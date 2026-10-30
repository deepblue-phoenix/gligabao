# MCP

gligabao can serve `remember` and `query` as tools of an [MCP](https://modelcontextprotocol.io) (Model Context Protocol) server, so that MCP clients (Claude Desktop, Claude Code, IDE agents, ...) can use it as their memory. It is built on the official Go SDK, [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk).

**It is off by default and has no authentication.** Enable it with `mcp = true` in the config file (`build/gligabao.toml`) or `serve --mcp`. Callers choose the `user_id` themselves, exactly as with the REST API when no `Auth` hook is set, so turn it on only on a trusted network. The endpoint is **not** covered by the [`Auth` hook](hooks.md#auth) (a warning is logged at startup if both are set); when gligabao is [mounted](../README.md) on a host's mux, `MountOptions.Wrap` does cover it, as it is under `/v1/gb/`.

## Endpoint

`/v1/gb/mcp`, the MCP *streamable HTTP* transport (`POST`; `GET` and `DELETE` are answered too, as the transport requires). The server is stateless: no session is kept between calls, and answers are plain JSON (no event stream).

```
claude mcp add --transport http gligabao http://localhost:7878/v1/gb/mcp
```

For a client that takes a JSON config:

```json
{"mcpServers": {"gligabao": {"type": "http", "url": "http://localhost:7878/v1/gb/mcp"}}}
```

## Tools

`remember` and `query`, and the three tools of the [research agents](#research-agents). Each runs the handler of the matching REST endpoint in process, so validation, limits, errors, monitoring events (`remember.completed`, `query.completed`, ...) and results are those of the endpoint; see its page for the details of every field.

### `remember`

Stores a text in memory, like [`POST /v1/gb/remember`](remember.md) with a text.

| Argument | | |
| -------- | - | - |
| `user_id` | required | the user that owns the dataset |
| `text` | required | the text to remember (at most 1 MiB) |
| `dataset` | optional | the dataset to store into, created if missing (default `main`) |
| `kind` | optional | `knowledge` (default) or `episode` |
| `session_id` | optional | a label for the user/agent session ([sessions.md](sessions.md)) |
| `reference_time` | optional | when the text was written, RFC 3339 (default now) |

The other options of the endpoint (`image`, `models`, `reasoning`, `web_search`, `timeseries`) are not available through MCP yet.

### `query`

Answers a question from the graph, like [`POST /v1/gb/query`](query.md).

| Argument | | |
| -------- | - | - |
| `user_id` | required | the user that owns the dataset |
| `query` | required | the question |
| `dataset` | optional | the dataset to search (default `main`) |
| `session_id` | optional | a label for the user/agent session |
| `reasoning` | optional | `none` (default), `low`, `medium` or `high` for the answering call, or an object with a level per stage: `{"query": "low", "detect_geo": "none"}` |
| `models` | optional | the model of each stage: `{"query": "...", "detect_geo": "..."}` |

### Research agents

Three tools run the handlers of the [research agent](research.md) endpoints: `research_start` is [`POST /v1/gb/research`](research.md#post-v1gbresearch), `research_list` is `GET /v1/gb/research` and `research_get` is `GET /v1/gb/research/{id}`. A research runs in the background and costs money (model calls and web searches): start it, then read it with `research_get` until its `status` is `completed` (the `report` is in it, with every `state` the agent went through) or `failed`.

| Tool | Arguments |
| ---- | --------- |
| `research_start` | `user_id` (required), `topic` (required), `max_steps` (optional, default 20, at most 40), `model` (optional), `dataset` (optional: the dataset of the knowledge base the agent queries and remembers into, default `main`) |
| `research_list` | `user_id` (required), `active` (optional: only the agents working now), `status` (optional: `running`, `completed` or `failed`), `limit` (optional) |
| `research_get` | `user_id` (required), `id` (required: the id `research_start` returned) |

### Results

On success the result is the JSON response of the endpoint, both as the tool's structured content and as text. If the endpoint answers with an error status (invalid argument `400`, dataset not found `404`, no LLM key `503`, failed LLM call `502`, ...) the result is a tool error (`isError: true`) whose text is the endpoint's message and status, so that the model can see what went wrong and correct the call. Arguments that do not match the tool's input schema (a missing `user_id` or `text`) are rejected by the MCP layer before the handler runs.

## Not included yet

Authentication, the other endpoints (datasets, timeseries, ontologies) as tools, cancelling a research, MCP resources and prompts. See the [roadmap](roadmap/ROADMAP.md).
