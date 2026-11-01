# Research agents

A **research agent** is a deep-research agent (a plain tool-calling loop, no framework; how it works is in [deep-research-agent.md](deep-research-agent.md)) that works on a topic in the background: over many steps it searches the web (Anthropic's web search tool) and reads pages (`fetch_url`), checks claims against more than one source, and ends with a report. It also works with the user's **internal knowledge base**: it `remember`s the important snippets it finds as it goes and `query`s the knowledge base besides the web (see [below](#the-knowledge-base)). gligabao runs it as a job: you start it, it returns at once with a run, and you follow what it does until it ends.

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
| `stopped` | somebody stopped it before it ended (from the [admin UI](#admin-ui)); `error` says who, and there is no report |

A run left `running` by a process that died is marked `failed` ("interrupted") when the server starts again.

A **state** (`gb_agent_research_states`) is one stage of its work. A row is inserted every time the agent moves from one state to the next; the table is append-only (a state is only ended, never rewritten), and `state` of the run is always the last row's. The states of a run:

| `state` | What the agent is doing | `detail` |
| ------- | ----------------------- | -------- |
| `starting` | the run is recorded, the agent has not made its first model call | |
| `thinking` | a model call is running: it decides what to do next | `step 3 of 20` |
| `searching` | the `web_search` tool is running | the query |
| `reading` | the `fetch_url` tool is running | the URL |
| `querying` | the `query` tool is running (the knowledge base) | the question |
| `remembering` | the `remember` tool is running | `async: ` or `sync: ` (whether the call waits), then the start of the text |
| `spawning` | the `spawn_subagent` tool is running ([sub-agents](subagents.md)) | the name of the sub-agent and its reason |
| `waiting` | the agent waits for its sub-agents (`wait_subagents`) or for a message (`read_messages` with `wait_seconds`) | the sub-agents, or `a message, up to N s` |
| `messaging` | `send_message`, `list_siblings` or `read_messages` (without waiting) is running | the sibling written to, and the start of the text |
| `using_tool` | any other tool is running | its name |
| `writing_report` | the last model call, the one that may not use tools, is running: its text is the report | `step 20 of 20, the last: ...` |
| `completed` / `failed` / `stopped` | the run ended (a terminal state, recorded too, so the states are the whole history) | |

The agent goes `starting`, then `thinking`, `searching`/`reading`/`querying`/`remembering` and `thinking` again, as many times as it needs, and ends: `thinking`, `completed` when the model answers before the last step; or `writing_report`, `completed` when it uses all its steps (the last step cannot call tools, so a run always ends with a report). A model that does not use tools in the first step goes `starting`, `thinking`, `completed`.

Columns of a state: `seq` (1, 2, 3, ... in the order entered), `step` (the model call it was on; 0 for `starting`, `completed` and `failed`), `detail`, `error`, `started_at` and `finished_at` (null for the state the run is in; millisecond precision). A tool that fails (a page that cannot be read) does not fail the run: the model is told and goes on, so the state has an `error` and the run can still complete. When a run fails, the state it failed in and the `failed` state both carry the error.

The run keeps `steps` (model calls made), `tool_calls`, `input_tokens`, `output_tokens` and `cost_usd`, updated after every model call and tool run. The cost includes the model calls of the web searches.

## REST

All three routes are user routes: `user_id` is required (in the body, or in the query string for `GET`), and with the [`Auth` hook](hooks.md#auth) the API key sets it. A user only sees their own runs: another user's run is a `404`.

### `POST /v1/gb/research`

Starts an agent. Body:

```json
{"user_id": "u1", "topic": "How do solid-state batteries compare with lithium-ion?", "max_steps": 20, "model": "", "dataset": "main"}
```

- `topic` (required, at most 2000 characters): what to research. A question, or a subject to write a report on.
- `max_steps` (optional, default 20, at most 40): the most model calls the agent makes, the last of which writes the report. More steps let it search and read more, and cost more.
- `model` (optional): the model it runs on, which must support Anthropic's web search tool. Default: the server's default model.
- `dataset` (optional, default `main`): the dataset of the user's knowledge base the agent queries and remembers into ([below](#the-knowledge-base)). It is created by the first `remember` if it does not exist; a bad name is a `400`. It is not stored with the run.

Answers `202` with the run (`status` `running`, `state` `starting`); the agent goes on in the background. `400` for a bad body, `409` if the user already has 3 agents working (wait for one to end), `503` without an Anthropic API key.

```json
{"id": 7, "user_id": "u1", "topic": "...", "status": "running", "state": "starting", "model": "", "max_steps": 20,
 "steps": 0, "tool_calls": 0, "input_tokens": 0, "output_tokens": 0, "cost_usd": 0, "report": "", "error": "",
 "created_at": "2026-11-01T10:00:00.120Z", "finished_at": null}
```

### `GET /v1/gb/research?user_id=...`

Lists the agents of the user, newest first, without their states: `{"runs": [...]}`.

- `active=true`: only the agents that are working now (`status` `running`).
- `status=running|completed|failed|stopped`: only the runs with that status (not together with `active`).
- `parent_id=<run id>`: only the [sub-agents](subagents.md) that run spawned.
- `limit`: at most this many (default 50, at most 500).

Sub-agents are runs like any other and are in the list too, with `parent_id`, `name` and `reason` set (`null`/`""` for a research a user started).

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

## Admin UI

The **research** view of the [admin UI](../README.md#admin-ui) shows the agents of the standard database, any user's:

- the number of agents working now, and of the ones that completed, failed or were stopped;
- **active agents**, each with a **Stop** button (it asks to confirm);
- **stopped and finished agents** (all, or only the completed, failed or stopped ones);
- for every agent, in its row: the number of **children** (sub-agents it spawned), its **running time** (from its start to its end, or to now while it works) and **how long ago** it started (minutes, hours, days or months);
- a **Details** modal per agent: whether it spawned sub-agents (each with its reason and status, to open or stop), its parent, its report and its states;
- for an agent that **failed**, a red **failed** button in its row that opens the error it failed with and the state it failed in;
- a **Knowledge** button per agent: its **provenance**, see below;
- a **Temporal plot** button in the **Details** modal (next to **Knowledge**): what the agent did to the facts over time, see below;
- a green **start agent** button that starts an agent, see below;
- a **dock** of the agents that work, see below.

**Starting.** The **start agent** button opens a modal with a tall text box for the task (at most 2000 characters; **Ctrl+Enter** submits). The agent is started as the user `admin`, with the defaults of [`POST /v1/gb/research`](#post-v1gbresearch) (20 steps, the server's model, the `main` dataset), and the modal closes when the server accepts it.

**The dock.** A block on the right of the screen, fixed so it stays while the page scrolls, for every agent that works: its state, topic, running time and steps. It is green while the agent is healthy (working or completed) and red when it failed, was stopped, or the server cannot be reached. An agent that ends while its block is shown keeps the block, with its final status, until it is dismissed. Clicking a block opens the details of the agent. The **Details** modal of a working agent redraws itself every few seconds and keeps its **States** section open and its scroll position.

**Stopping.** `Manager.Stop` cancels the context of the agent, so its model call or tool is interrupted; its run ends as `stopped` (error `stopped by the admin UI`, no report) and so do its sub-agents (error `stopped: the agent that spawned it (run N) was stopped by ...`). Stopping a sub-agent leaves its parent working: the parent is told, by `wait_subagents`, that it did not finish. An agent that has ended cannot be stopped (`409`). Text the agent queued with `remember` is not tied to the run, so it is still absorbed. There is no user route for it yet, only the admin one.

**Provenance.** Everything the `remember` tool stores carries the session `research-<run id>` (`agent_research.SessionID`), and the graph records it on the artifacts (`gb_artifacts.session_id`), on every entity that session discovered (`gb_entity_sessions`) and on every fact (`gb_edge_sessions`), also when the entity or fact was stored before and the agent only found it again. The provenance of an agent is what those tables hold for its session, in any dataset (the run does not store its dataset): the artifacts (texts, chunks, web search results), the entities and the facts, each with the datasets they are in, and, for facts, whether an artifact of the agent created it (`created`) or the agent restated it. With `include_subagents` the sessions of its sub-agents are included and each item says which agent it comes from. The modal can open the graph of a dataset, or draw a graph of only what the agent contributed.

**Temporal plot.** The **Temporal plot** button of the details modal opens the same plot as the **Temporal changes** of a [remember run](remember.md#admin-ui): what the agent did to the facts over event time (created, confirmed, ended, invalidated, superseded). It covers the whole session `research-<run id>` in whichever dataset the agent remembered (the artifacts and events of each event are read from the dataset of that event, so a plot can span several datasets), and the sub-agents' sessions too when the agent has any. The queries the agent asked (`query`) carry the same session in the query run log (`gb_query_runs`).

Endpoints (admin routes, under `/v1/gb/admin/api/`, not tied to a user): `GET research[?status=running|completed|failed|stopped|finished][&user_id=][&roots_only=true][&limit=]` (the counts of all the agents by status and the agents that match, each with `children`, `duration_ms`, `age_ms` and `has_report`, without their reports), `GET research/{id}` (the agent with its report, states, parent and sub-agents), `POST research` (starts an agent like `POST /v1/gb/research`, with the body `{"topic": "", "user_id": "admin", "max_steps": 20, "model": "", "dataset": ""}`, only `topic` required; answers `202` with the agent, and `400`, `409` and `503` as there), `POST research/{id}/stop`, `GET research/{id}/provenance[?include_subagents=true]` (`datasets`, `artifacts`, `entities` and `edges`) and `GET research/{id}/temporal[?include_subagents=true]` (`artifacts` and `events`, as in `GET remember-run-temporal`). They are mounted with the admin UI (`internal/api/admin/research.go`), and only read or stop the agents of the server's own database.

## The knowledge base

Besides the web, the agent works with the user's internal knowledge base (the graph memory of [remember](remember.md) and [query](query.md)), through two more tools (details in [deep-research-agent.md](deep-research-agent.md#tool-query)):

- **`remember`** (`text`, optional `source`, optional `sync`): while it searches the web and reads pages, the agent submits the important parts (short snippets with their context, the URL as the source) to the [remember manager](remember-manager.md), as the user, into the run's dataset. **`sync` toggles whether the call blocks.** By default (`sync=false`) the call only queues the text and returns at once, so the agent goes on without waiting for it to be absorbed; with `sync=true` it waits until the text is in the knowledge base, for when the agent needs it right away (to `query` for it).
- **`query`** (`question`): besides web searches and page reads, the agent asks the knowledge base what it knows already, and checks what it found against it. It gets the answer and the facts it is based on. An empty (not yet existing) dataset answers that it is empty.

The tools are on whenever the server has an LLM for the memory (the same condition as the research itself); the remember and query calls are made with the options of the `remember` and `query` endpoints and the session `research-<run id>`, so they show in the remember run log (`gb_remember_runs`) and query run log with it. Text that is queued is not tied to the run: it is absorbed even after the run ended, and a failure of it does not fail the run. The cost of these calls is recorded as inferences (`gb_inferences`) and is not part of the run's `cost_usd`.

## Sub-agents

A research agent can spawn **sub-agents**, other research agents that take sub-tasks (`spawn_subagent`, `wait_subagents`), and sibling sub-agents can see each other and their reasons (`list_siblings`) and message each other (`send_message`, `read_messages`). Every sub-agent is spawned with a reason. A sub-agent is a run like any other: it has its own states, usage and report, and a `parent_id`, a `name` and a `reason` (`null`/`""` for a research a user started); `GET /v1/gb/research?parent_id=` lists the sub-agents of a run. They are bounded (5 per agent, 10 per research, 2 levels deep) and are cancelled when their parent ends. All of it, the tools, the limits, who can message whom and the replaceable message system, is in [subagents.md](subagents.md).

## How it works

- `internal/agent`: the loop and the research agent ([deep-research-agent.md](deep-research-agent.md)). It has an `Observer` (before and after each model call and each tool run) that the manager uses to record the states, and `ReportUsage`, with which a tool that calls a model (the web search) adds its tokens and cost to the run.
- `internal/agent_research`: the `Manager` makes a `researcher` for each agent, the struct that holds all of its state (see [isolation](deep-research-agent.md#isolation)), and starts it in a goroutine of its own (at most 3 working at once per user); the manager creates the run and its first state in one transaction, and the researcher enters a state in a transaction at each event (end the previous state, insert the new one, set the run's `state`), writes the usage after every model call and tool run, and ends the run (its status, report or error, and the terminal state) in a transaction. A failed write of the progress is logged and does not stop the agent. Storage is in `agent_research/db/sqlite` and `agent_research/db/postgres` (each with its schema); the types are in `agent_research/core`.
- `internal/agent_research/team.go` and `messages.go`, `internal/agent/team.go`: the sub-agents and their messages; the prompts are in `internal/agent/prompts/` (see [subagents.md](subagents.md)).
- `internal/agent_memory/memory.go`: `Memory` (the remember manager, the database, the LLM and the options of the remember and query endpoints), which `api.Register` gives the manager (`Manager.UseMemory`; `agent_research.Memory` is an alias of it); each agent gets its own view of it (`Memory.Knowledge`: its user, dataset and session) that implements `agent.Knowledge`, the interface behind the tools `remember` and `query` (`internal/agent/knowledge.go`). The package is shared with the standalone binary ([deep-research-agent.md](deep-research-agent.md#the-standalone-binary-gligabao_agent)). Without a `Memory` the agents only have the web tools.
- `internal/api/research`: the REST handlers; `internal/api/mcp` and `cli` call them (the MCP tools in process, the CLI over HTTP).
- When the server stops, the agents are cancelled and their runs are ended as `failed` ("interrupted") before the databases close.

## Not included yet

Cancelling a run by a user (only the admin UI can stop one), messages between agents that are not siblings (a parent and its sub-agents, cousins), storing the messages, a stream of its states (poll `GET /v1/gb/research/{id}`), storing the report itself in a dataset, waiting for the queued snippets before a run ends, counting the cost of `remember` and `query` in the run's cost, recording the agent's model calls in `gb_inferences`, and a cap on the cost of a run. See the [roadmap](roadmap/ROADMAP.md).
