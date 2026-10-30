# Research agents

A **research agent** is a deep-research agent (`internal/agent`: a plain tool-calling loop, no framework) that works on a topic in the background: over many steps it searches the web (Anthropic's web search tool) and reads pages (`fetch_url`), checks claims against more than one source, and ends with a report. gligabao runs it as a job: you start it, it returns at once with a run, and you follow what it does until it ends.

Every run is stored in the relational database, and so is **every state the agent goes through while it works** (searching a query, reading a page, waiting for the model, ...), so its progress can be followed in detail, and kept afterwards.

It needs an Anthropic API key (else `503`). Each run costs money: up to `max_steps` model calls plus the web searches (a fee for each, on top of the tokens, see [web-search.md](web-search.md)). The cost is recorded and updated as the run goes.

Access: REST ([below](#rest)), [MCP](mcp.md#research-agents) (`research_start`, `research_list`, `research_get`) and the [CLI](cli.md) (`gligabao research start|ls|get`). They all use the same handlers, so they behave the same.

## Runs and states

A **run** (`gb_agent_research_runs`) is one execution of the agent on a topic. Its `status` is its lifecycle:

| `status` | Meaning |
| -------- | ------- |
| `running` | the agent is working (an **active** agent) |
| `completed` | it ended with a report (`report`) |
| `failed` | it could not finish: a model call failed, or the server stopped (`error` says why) |

A run left `running` by a process that died is marked `failed` ("interrupted") when the server starts again.

A **state** (`gb_agent_research_states`) is one stage of its work. A row is inserted every time the agent moves from one state to the next; the table is append-only (a state is only ended, never rewritten), and `state` of the run is always the last row's. The states of a run:

| `state` | What the agent is doing | `detail` |
| ------- | ----------------------- | -------- |
| `starting` | the run is recorded, the agent has not made its first model call | |
| `thinking` | a model call is running: it decides what to do next | `step 3 of 20` |
| `searching` | the `web_search` tool is running | the query |
| `reading` | the `fetch_url` tool is running | the URL |
| `using_tool` | any other tool is running | its name |
| `writing_report` | the last model call, the one that may not use tools, is running: its text is the report | `step 20 of 20, the last: ...` |
| `completed` / `failed` | the run ended (a terminal state, recorded too, so the states are the whole history) | |

The agent goes `starting`, then `thinking`, `searching`/`reading` and `thinking` again, as many times as it needs, and ends: `thinking`, `completed` when the model answers before the last step; or `writing_report`, `completed` when it uses all its steps (the last step cannot call tools, so a run always ends with a report). A model that does not use tools in the first step goes `starting`, `thinking`, `completed`.

Columns of a state: `seq` (1, 2, 3, ... in the order entered), `step` (the model call it was on; 0 for `starting`, `completed` and `failed`), `detail`, `error`, `started_at` and `finished_at` (null for the state the run is in; millisecond precision). A tool that fails (a page that cannot be read) does not fail the run: the model is told and goes on, so the state has an `error` and the run can still complete. When a run fails, the state it failed in and the `failed` state both carry the error.

The run keeps `steps` (model calls made), `tool_calls`, `input_tokens`, `output_tokens` and `cost_usd`, updated after every model call and tool run. The cost includes the model calls of the web searches.

## REST

All three routes are user routes: `user_id` is required (in the body, or in the query string for `GET`), and with the [`Auth` hook](hooks.md#auth) the API key sets it. A user only sees their own runs: another user's run is a `404`.

### `POST /v1/gb/research`

Starts an agent. Body:

```json
{"user_id": "u1", "topic": "How do solid-state batteries compare with lithium-ion?", "max_steps": 20, "model": ""}
```

- `topic` (required, at most 2000 characters): what to research. A question, or a subject to write a report on.
- `max_steps` (optional, default 20, at most 40): the most model calls the agent makes, the last of which writes the report. More steps let it search and read more, and cost more.
- `model` (optional): the model it runs on, which must support Anthropic's web search tool. Default: the server's default model.

Answers `202` with the run (`status` `running`, `state` `starting`); the agent goes on in the background. `400` for a bad body, `409` if the user already has 3 agents working (wait for one to end), `503` without an Anthropic API key.

```json
{"id": 7, "user_id": "u1", "topic": "...", "status": "running", "state": "starting", "model": "", "max_steps": 20,
 "steps": 0, "tool_calls": 0, "input_tokens": 0, "output_tokens": 0, "cost_usd": 0, "report": "", "error": "",
 "created_at": "2026-11-01T10:00:00.120Z", "finished_at": null}
```

### `GET /v1/gb/research?user_id=...`

Lists the agents of the user, newest first, without their states: `{"runs": [...]}`.

- `active=true`: only the agents that are working now (`status` `running`).
- `status=running|completed|failed`: only the runs with that status (not together with `active`).
- `limit`: at most this many (default 50, at most 500).

### `GET /v1/gb/research/{id}?user_id=...`

Reads one run with its `states`, in the order they were entered. The `report` is in it once `status` is `completed`.

```json
{"id": 7, "status": "completed", "state": "completed", "steps": 5, "tool_calls": 6, "cost_usd": 0.13, "report": "# ...",
 "states": [
   {"seq": 1, "state": "starting", "step": 0, "detail": "", "error": "", "started_at": "...", "finished_at": "..."},
   {"seq": 2, "state": "thinking", "step": 1, "detail": "step 1 of 20", "error": "", "started_at": "...", "finished_at": "..."},
   {"seq": 3, "state": "searching", "step": 1, "detail": "solid-state battery energy density 2025", "error": "", "started_at": "...", "finished_at": "..."},
   {"seq": 4, "state": "thinking", "step": 2, "detail": "step 2 of 20", "error": "", "started_at": "...", "finished_at": "..."},
   {"seq": 5, "state": "reading", "step": 2, "detail": "https://example.com/paper", "error": "example.com/paper: status 404", "started_at": "...", "finished_at": "..."},
   "..."
 ]}
```

To follow a run, poll this route (or `research_get`, or `gligabao research get`).

## CLI

```
gligabao research start TOPIC [--max-steps N] [--model M] [--wait [--raw]] [--json BODY]
gligabao research ls [--active] [--status S] [--limit N]
gligabao research get ID [--raw]
```

`start` prints the run right away; with `--wait` it waits for the agent to end and prints the finished run (exit code 1 if it failed), and `--raw` prints only the report. `get --raw` prints only the report. See [cli.md](cli.md).

## How it works

- `internal/agent`: the loop. It has an `Observer` (before and after each model call and each tool run) that the manager uses to record the states, and `ReportUsage`, with which a tool that calls a model (the web search) adds its tokens and cost to the run.
- `internal/agent_research`: the `Manager` starts an agent in a goroutine (at most 3 working at once per user), creates the run and its first state in one transaction, enters a state in a transaction at each event (end the previous state, insert the new one, set the run's `state`), writes the usage after every model call and tool run, and ends the run (its status, report or error, and the terminal state) in a transaction. A failed write of the progress is logged and does not stop the agent. Storage is in `agent_research/db/sqlite` and `agent_research/db/postgres` (each with its schema); the types are in `agent_research/core`.
- `internal/api/research`: the REST handlers; `internal/api/mcp` and `cli` call them (the MCP tools in process, the CLI over HTTP).
- When the server stops, the agents are cancelled and their runs are ended as `failed` ("interrupted") before the databases close.

## Not included yet

Cancelling a run, a stream of its states (poll `GET /v1/gb/research/{id}`), storing the report in a dataset (`remember`), recording the agent's model calls in `gb_inferences`, a view in the admin UI, and a cap on the cost of a run. See the [roadmap](roadmap/ROADMAP.md).
