# Research agents

A **research agent** is a deep-research agent (a plain tool-calling loop, no framework; how it works is in [deep-research-agent.md](../deep-research-agent.md)) that works on a topic in the background: over many steps it searches the web (Anthropic's web search tool) and reads pages (`fetch_url`), checks claims against more than one source, and ends with a report. It also works with the user's **internal knowledge base**: it `remember`s the important snippets it finds as it goes and `query`s the knowledge base besides the web (see [below](#the-knowledge-base)). gligabao runs it as a job: you start it, it returns at once with a run, and you follow what it does until it ends.

Every run is stored in the relational database, and so is **every state the agent goes through while it works** (searching a query, reading a page, waiting for the model, ...), so its progress can be followed in detail, and kept afterwards.

It needs an Anthropic API key (else `503`). Each run costs money: up to `max_steps` model calls plus the web searches (a fee for each, on top of the tokens, see [web-search.md](../web-search.md)). The cost is recorded and updated as the run goes.

By default the agent runs **in the server's process** (the `local` runner). With `"runner": "modal"` it runs instead as the `gligabao_agent` binary **in Modal**, started through the sidecar the way training is ([below](#where-the-agent-runs-the-runner)).

Access: REST ([below](#rest)), [MCP](../mcp.md#research-agents) (`research_start`, `research_list`, `research_get`) and the [CLI](../cli.md) (`gligabao research start|ls|get`). They all use the same handlers, so they behave the same.

## Runs and states

A **run** (`gb_agent_research_runs`) is one execution of the agent on a topic. Its `status` is its lifecycle:

| `status` | Meaning |
| -------- | ------- |
| `running` | the agent is working (an **active** agent) |
| `completed` | it ended with a report (`report`) |
| `failed` | it could not finish: a model call failed, or the server stopped (`error` says why) |
| `stopped` | somebody stopped it before it ended (from the [admin UI](#admin-ui)); `error` says who, and there is no report |

A run left `running` by a process that died is marked `failed` ("interrupted") when the server starts again (the job of a run in Modal is not followed again after a restart: see [the runner](#where-the-agent-runs-the-runner)).

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
| `waiting` | the agent waits for its sub-agents (`wait_subagents`) | the sub-agents |
| `using_tool` | any other tool is running | its name |
| `importing` | only for a `modal` run: the agent ended and the server loads what it learned into its databases ([below](#what-comes-back-the-import)) | `loading what the agent learned (1.2 MB)`; the `error` says why it failed |
| `writing_report` | the last model call, the one that may not use tools, is running: its text is the report | `step 20 of 20, the last: ...` |
| `completed` / `failed` / `stopped` | the run ended (a terminal state, recorded too, so the states are the whole history) | |

The agent goes `starting`, then `thinking`, `searching`/`reading`/`querying`/`remembering` and `thinking` again, as many times as it needs, and ends: `thinking`, `completed` when the model answers before the last step; or `writing_report`, `completed` when it uses all its steps (the last step cannot call tools, so a run always ends with a report). A model that does not use tools in the first step goes `starting`, `thinking`, `completed`.

Columns of a state: `seq` (1, 2, 3, ... in the order entered), `step` (the model call it was on; 0 for `starting`, `completed` and `failed`), `detail`, `error`, `started_at` and `finished_at` (null for the state the run is in; millisecond precision). A tool that fails (a page that cannot be read) does not fail the run: the model is told and goes on, so the state has an `error` and the run can still complete. When a run fails, the state it failed in and the `failed` state both carry the error.

The run keeps `steps` (model calls made; for an [investigative run](#the-investigative-loop) the turns), `tool_calls`, `input_tokens`, `output_tokens` and `cost_usd`, updated after every model call and tool run. The cost includes the model calls of the web searches.

## REST

All three routes are user routes: the user is the `gb-user-id` header (required; never read from the body or the query string), and with the [`Auth` hook](../hooks.md#auth) the API key sets it. A user only sees their own runs: another user's run is a `404`.

### `POST /v1/gb/research`

Starts an agent. Body:

```json
{"topic": "How do solid-state batteries compare with lithium-ion?", "agent": "default", "max_steps": 20, "model": "", "max_cost_usd": 0, "dataset": "main", "runner": "local"}
```

- `topic` (required, at most 2000 characters): what to research. A question, or a subject to write a report on.
- `agent` (optional, default `default`): the [agent definition](agent-defs.md) to run (`GET /v1/gb/research/agents` lists them); an unknown one is a `400`. `agent_version` (optional) is a constraint its version must meet (`1.2.0`, `>=1.0`, `^1.2`); a mismatch is a `400`. The run records `agent_name`, `agent_version` and `agent_hash`.
- `max_cost_usd` (optional, 0 = none): a cap in USD on what the whole research may spend, sub-agents included. It can lower the caps of the definition and of the server (`research_max_cost_usd`), never raise them. When it is reached the agents write their reports and the report says so ([cost caps](agent-defs.md#limits-cost-caps)).
- `max_steps` (optional, default: the `max_steps` of the definition, 20 for `default`; at most 40): the most model calls the agent makes, the last of which writes the report. More steps let it search and read more, and cost more.
- `model` (optional): the model it runs on, which must support Anthropic's web search tool. Default: the `model` of the definition, else the server's default model. It is the model of the main agent; a sub-agent runs on the model of its own definition, else its parent's.
- `dataset` (optional, default `main`): the dataset of the user's knowledge base the agent queries and remembers into ([below](#the-knowledge-base)). It is created by the first `remember` if it does not exist; a bad name is a `400`. It is not stored with the run. With the `modal` runner it is the dataset the agent's knowledge lands in when the agent ends ([below](#what-comes-back-the-import)).
- `investigate` (optional): the knobs of the [investigative loop](#the-investigative-loop) (`max_iterations`, `max_total_tokens`, `min_sources`, ... the same names as the `[investigate]` table of the [agent definition](agent-defs.md#loop-and-investigate-the-investigative-loop)), over those of the definition. Only for an agent with `loop = "investigative"`: with the simple loop they are a `400`. With that loop `max_steps` is a `400` too (`max_iterations` replaces it), and so is `runner: "modal"`.
- `runner` (optional, default `local`): where the agent runs, `local` (in the server, as always) or `modal` (the `gligabao_agent` binary in a Modal container, see [below](#where-the-agent-runs-the-runner)). Any other value is a `400`. The run says which one it has (`runner`).

Answers `202` with the run (`status` `running`, `state` `starting`); the agent goes on in the background. `400` for a bad body, `409` if the user already has 3 agents working (wait for one to end), `503` without an Anthropic API key (for `local`), or, for `modal`, when the sidecar cannot start the job (it is down, or the Modal app has no agent: the message says which).

```json
{"id": 7, "user_id": "u1", "topic": "...", "status": "running", "state": "starting", "model": "", "max_steps": 20, "runner": "local", "agent_name": "default", "agent_version": "1.0.0", "agent_hash": "9f2c...",
 "steps": 0, "tool_calls": 0, "input_tokens": 0, "output_tokens": 0, "cost_usd": 0, "report": "", "error": "",
 "created_at": "2026-11-01T10:00:00.120Z", "finished_at": null}
```

### `GET /v1/gb/research/agents`

`{"agents": [...]}`: the [agent definitions](agent-defs.md) a research can run, by name, each with `name`, `version`, `description`, `hash`, `source` (the file, or `builtin`), `model`, `max_steps`, `max_cost_usd`, `max_tree_cost_usd`, `knowledge`, `skills` and `subagents` (the roles).

### `GET /v1/gb/research`

Lists the agents of the user, newest first, without their states: `{"runs": [...]}`.

- `active=true`: only the agents that are working now (`status` `running`).
- `status=running|completed|failed|stopped`: only the runs with that status (not together with `active`).
- `parent_id=<run id>`: only the [sub-agents](subagents.md) that run spawned.
- `limit`: at most this many (default 50, at most 500).

Sub-agents are runs like any other and are in the list too, with `parent_id`, `name` and `reason` set (`null`/`""` for a research a user started).

### `GET /v1/gb/research/{id}`

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
gligabao research start TOPIC [--agent NAME] [--agent-version V] [--max-cost USD] [--max-steps N] [--model M] [-d DATASET] [--runner local|modal] [--wait [--raw]] [--json BODY]
gligabao research ls [--active] [--status S] [--limit N]
gligabao research get ID [--raw]
gligabao research agents
```

`--runner modal` runs the agent in Modal ([below](#where-the-agent-runs-the-runner)); the default is local. `start` prints the run right away; with `--wait` it waits for the agent to end and prints the finished run (exit code 1 if it failed), and `--raw` prints only the report. `get --raw` prints only the report. See [cli.md](../cli.md).

## Admin UI

The **research** view of the [admin UI](../../README.md#admin-ui) shows the agents of the standard database, any user's:

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

**Starting.** The **start agent** button opens a modal with a tall text box for the task (at most 2000 characters; **Ctrl+Enter** submits). The modal also has a **Where it runs** choice: *local* (the default) or *Modal*. The agent is started as the user `admin`, with the defaults of [`POST /v1/gb/research`](#post-v1gbresearch) (20 steps, the server's model, the `main` dataset), and the modal closes when the server accepts it. The **Details** modal of an agent says where it runs.

**The dock.** A block on the right of the screen, fixed so it stays while the page scrolls, for every agent that works: its state, topic, running time and steps. It is green while the agent is healthy (working or completed) and red when it failed, was stopped, or the server cannot be reached. An agent that ends while its block is shown keeps the block, with its final status, until it is dismissed. Clicking a block opens the details of the agent. The **Details** modal of a working agent redraws itself every few seconds and keeps its **States** section open and its scroll position.

**Stopping.** `Manager.Stop` cancels the context of the agent, so its model call or tool is interrupted; its run ends as `stopped` (error `stopped by the admin UI`, no report) and so do its sub-agents (error `stopped: the agent that spawned it (run N) was stopped by ...`). Stopping a sub-agent leaves its parent working: the parent is told, by `wait_subagents`, that it did not finish. An agent that has ended cannot be stopped (`409`). Text the agent queued with `remember` is not tied to the run, so it is still absorbed. There is no user route for it yet, only the admin one.

**Provenance.** Everything the `remember` tool stores carries the session `research-<run id>` (`agent_research.SessionID`), and the graph records it on the artifacts (`gb_artifacts.session_id`), on every entity that session discovered (`gb_entity_sessions`) and on every fact (`gb_edge_sessions`), also when the entity or fact was stored before and the agent only found it again. The provenance of an agent is what those tables hold for its session, in any dataset (the run does not store its dataset): the artifacts (texts, chunks, web search results), the entities and the facts, each with the datasets they are in, and, for facts, whether an artifact of the agent created it (`created`) or the agent restated it. With `include_subagents` the sessions of its sub-agents are included and each item says which agent it comes from. The modal can open the graph of a dataset, or draw a graph of only what the agent contributed.

**Temporal plot.** The **Temporal plot** button of the details modal opens the same plot as the **Temporal changes** of a [remember run](../remember/remember.md#admin-ui): what the agent did to the facts over event time (created, confirmed, ended, invalidated, superseded). It covers the whole session `research-<run id>` in whichever dataset the agent remembered (the artifacts and events of each event are read from the dataset of that event, so a plot can span several datasets), and the sub-agents' sessions too when the agent has any. The queries the agent asked (`query`) carry the same session in the query run log (`gb_query_runs`).

Endpoints (admin routes, under `/v1/gb/admin/api/`, not tied to a user): `GET research[?status=running|completed|failed|stopped|finished][&user_id=][&roots_only=true][&limit=]` (the counts of all the agents by status and the agents that match, each with `children`, `duration_ms`, `age_ms` and `has_report`, without their reports), `GET research/{id}` (the agent with its report, states, parent and sub-agents), `POST research` (starts an agent like `POST /v1/gb/research`, with the body `{"topic": "", "user_id": "admin", "max_steps": 20, "model": "", "dataset": "", "runner": "local"}`, only `topic` required; answers `202` with the agent, and `400`, `409` and `503` as there), `POST research/{id}/stop`, `GET research/{id}/provenance[?include_subagents=true]` (`datasets`, `artifacts`, `entities` and `edges`) and `GET research/{id}/temporal[?include_subagents=true]` (`artifacts` and `events`, as in `GET remember-run-temporal`). They are mounted with the admin UI (`internal/api/admin/research.go`), and only read or stop the agents of the server's own database.

## Where the agent runs (the runner)

A research has a **runner**, chosen when it is started (`runner` in the REST body, `--runner` in the CLI, `runner` in `research_start`, the choice in the admin UI):

| Runner | What it is |
| ------ | ---------- |
| `local` (the default) | the agent is a goroutine of the server, as described everywhere else on this page |
| `modal` | the agent is the **`gligabao_agent` binary running in a Modal container**, started through the sidecar, the way training jobs are |

The server never talks to Modal: for `modal` it asks the [sidecar](../training/sidecar.md) (`POST /research/job_start`), which spawns a function of the same Modal app that trains ([modal.md](../training/modal.md)), whose container runs the binary ([deep-research-agent.md](../deep-research-agent.md#the-standalone-binary-gligabao_agent)) on the topic, with the sub-agents inside it. The server records the run exactly as for a local one (`gb_agent_research_runs`, with `runner` `modal`), follows the job's event stream (SSE, like a training job's) and writes what the container reports to the run: the states the agents enter, the usage and, in the end, the report. REST, MCP, the CLI and the admin UI see the same run and the same states; only `runner` differs.

```
server ── POST /research/job_start ──► sidecar ──► Modal app: run_agent (container with the gligabao_agent binary)
   ▲                                                  │  the binary writes its progress (--events) to stderr;
   └──── GET /research/job_events (SSE) ◄─────────────┘  the container puts it in the job's event log (a Modal Dict)
   └──── GET /research/job_export ◄───────────────────────  when it ends: the archive of what the agent learned (below)
```

**What differs from a local run:**

- **The sub-agents run inside the container**, so while the research works the states they enter are in the timeline of the run, with the name of the sub-agent in front of the detail (`[worker] https://...`), and the run's `steps`, `tool_calls`, tokens and `cost_usd` are those of the **whole tree** (as the totals of `gligabao_agent`), updated about every half second as the container reports them. When the agent ends, the runs of the sub-agents are imported as runs of their own (children of the run, listed by `GET /v1/gb/research?parent_id=`), see [below](#what-comes-back-the-import).
- **The knowledge base is the container's own while the agent works**: empty at the start (it is not a copy of the user's) and gone when the container ends. The `remember` and `query` tools work inside the run, and what the agent remembered **comes back to the user's knowledge base when it ends** ([below](#what-comes-back-the-import)); until then provenance and the temporal plot of the admin UI show nothing for the run, and the agent cannot see what the user's dataset holds.
- **The keys are the Modal app's.** The container uses the keys of the Modal secret named by `SIDECAR_AGENT_SECRET` (`ANTHROPIC_API_KEY` and `OPENAI_API_KEY`), not the server's: the server needs no Anthropic key to start a `modal` research. The `model` must still support Anthropic's web search tool.
- **No per-process limit but the user's.** The limit of 3 agents working at once per user counts the runs in Modal too.
- **Stopping** (admin UI, `Manager.Stop`) asks the sidecar to cancel the job; the container ends the binary (`SIGTERM`, which ends the research properly; `SIGKILL` after 20 seconds) and the run ends as `stopped` when the job reports it. If the sidecar cannot be reached the run ends as `stopped` at once, with a note that the job may go on in Modal until its timeout (`SIDECAR_AGENT_TIMEOUT_S`, 30 minutes by default). The same happens when the server stops: it asks for the job to be cancelled before it ends the run as `failed` ("interrupted").
- **A restart of the server does not reattach to the job**: runs still `running` are marked `failed` when it starts again (as for local runs), and a job that is still going in Modal runs until it ends or times out. Its cost is not recorded, and neither is what it learns: nobody is left to download its export.
- **Failures are as visible as for a local run**: a job that dies without reporting (out of memory, the timeout) is closed by the sidecar's watchdog with a `failed` event, and a follower that cannot reach the sidecar for 5 minutes gives up and ends the run as `failed` ("lost contact"). A job may take 3 hours at most.

### What comes back (the import)

The container and its storage are gone when the job ends, so the agent hands its state over: the Modal function runs the binary with `--export` ([deep-research-agent.md](../deep-research-agent.md#the-export-gligabao_agent---export)), stores the archive in the job's event log (in chunks, before the final event, which says its size and SHA-256), and the sidecar serves it (`GET /research/job_export`). Also a job that **failed or was stopped** hands its state over: what it learned until then is kept. When the follower of the run sees the final event it enters the state `importing`, downloads the archive (3 attempts, checked against the size and the SHA-256), has the importer load it (`internal/db/dbimport`) and only then ends the run, so a run that says `completed` already has its knowledge in. A download or import that fails does not fail the run: the report is the result, the `importing` state carries the error (`the export of the agent was not imported: ...`) and the log has the details. A server without an importer, or a job with no export (a binary that could not write it), skips the step.

What is loaded, into the databases the server uses (SQLite or PostgreSQL, Chromem or Pinecone), as the user that started the research:

| From the agent | Becomes |
| -------------- | ------- |
| datasets (`gb_datasets`) | the user's dataset of the same name (created if it does not exist; the `dataset` of the research is the usual one). A dataset deleted in the container is left out. |
| entities, time series | **merged** into those the dataset has: an entity by name key and type, a series by provider, key and field. The stored row is kept (its description, its summary), and what the agent found about it is added: the artifacts and facts that mention it, its place, the new points of a series (those it has stay; the summary of the series is recomputed). |
| artifacts, chunks and summaries, facts (`gb_edges`) and their events, classes, images (rows and files), series anchors | new rows, with every reference remapped. The image files go to the image directory, under the dataset's ID. |
| model calls and models (`gb_inferences`, `gb_models`) | new rows: what the agents spent in the container is in the server's call log, with the costs. A model the server does not know is added as unsupported. |
| remember and query runs, their stage outputs, the log of what went into prompts | new rows, with the artifact, the inference IDs and the session inside them remapped. |
| the runs of the sub-agents and their states | runs of their own, children of the server's run (`runner` `modal`, the status and report they ended with; one that was still working when the container ended is `failed`). The run and the states of the main agent are the server's own and are not imported again. |
| sessions `research-<id>` | renamed to the IDs the runs have on the server, so provenance and the temporal plot work as for a local run. |
| embeddings | into the vector store of the server (the vector datasets of the datasets above), under the new IDs and with the IDs in their metadata remapped. The vector of a merged entity is not copied over the one the server has. Only copied when the agent's embedding size is the server's. |

The rows of the relational database go in **one transaction**: an import that fails leaves it as it was. The vectors and the image files are written after it commits; a failure there is only logged (the rows are in). Importing the same archive twice would add its rows twice, but a run is only imported once. The tables of the ontologies are not imported (an agent has none).

**Setting it up.**

1. Build the agent for the containers: `./run_build.sh` makes `build/gligabao_agent_linux_amd64` (a static Linux x86-64 binary, whatever the machine). The sidecar image (`Dockerfile.sidecar`) builds it itself and has it next to `ai_train/`; `SIDECAR_AGENT_BINARY` points the sidecar at another file.
2. Create the Modal secret with the keys: `modal secret create gligabao-agent ANTHROPIC_API_KEY=... OPENAI_API_KEY=...` and set `SIDECAR_AGENT_SECRET=gligabao-agent` for the sidecar. (Without a secret the agent runs without keys and fails with a message saying which one is missing.)
3. **Deploy the Modal app again** (the admin UI's training view, or `modal deploy app.py`): the deploy bakes the binary into the agent's image. An app deployed without the binary has no agent: starting a `modal` research answers `503` ("this Modal app has no research agent ...").
4. Start a research with `runner: "modal"`. The server only needs a reachable sidecar (`sidecar_url`).

`SIDECAR_AGENT_TIMEOUT_S` (default 1800) is the timeout of the container's function; the binary is told to end 90 seconds before it so that it finishes by itself, with a report if it can. More about the Modal side (the function, the event protocol, the configuration) is in [training/sidecar.md](../training/sidecar.md#the-research-agent-in-modal).

Code: `internal/agent_research/remote.go` (the `Remote` and `Importer` interfaces the manager runs the `modal` runner and loads its export through, and the follower that writes the job's events to the run), `internal/sys/server/research_remote.go` (their implementation over the sidecar client and the server's databases), `internal/db/dbimport` (the import), `internal/sys/sidecar/research_jobs.go` (the client), `sidecar/ai_train/ai_train_core/agent_run.py` (the container side).

## The knowledge base

Besides the web, the agent works with the user's internal knowledge base (the graph memory of [remember](../remember/remember.md) and [query](../query/query.md)), through two more tools (details in [deep-research-agent.md](../deep-research-agent.md#tool-query)):

- **`remember`** (`text`, optional `source`, optional `sync`): while it searches the web and reads pages, the agent submits the important parts (short snippets with their context, the URL as the source) to the [remember manager](../remember/remember-manager.md), as the user, into the run's dataset. **`sync` toggles whether the call blocks.** By default (`sync=false`) the call only queues the text and returns at once, so the agent goes on without waiting for it to be absorbed; with `sync=true` it waits until the text is in the knowledge base, for when the agent needs it right away (to `query` for it).
- **`query`** (`question`): besides web searches and page reads, the agent asks the knowledge base what it knows already, and checks what it found against it. It gets the answer and the facts it is based on. An empty (not yet existing) dataset answers that it is empty.

The tools are on whenever the server has an LLM for the memory (the same condition as the research itself); the remember and query calls are made with the options of the `remember` and `query` endpoints and the session `research-<run id>`, so they show in the remember run log (`gb_remember_runs`) and query run log with it. Text that is queued is not tied to the run: it is absorbed even after the run ended, and a failure of it does not fail the run. The cost of these calls is recorded as inferences (`gb_inferences`) and is not part of the run's `cost_usd`.

## The investigative loop

An agent whose definition says `loop = "investigative"` runs the [deep-research loop](deep-research-loop.md) instead of the simple one (the simple loop is the default and does not change). It works in two phases on a frontier of questions with a notes file, and a run records where it is:

- `phase` of the run (`gb_agent_research_runs.phase`): `exploration` (the cycle on the frontier) or `synthesis` (the report is written from the notes, without search tools); `''` for a run of the simple loop and for runs made before 0.181.0.
- `stop_reason`: `''` while the run explores normally; `frontier_exhausted` (no actionable question was left and the final check queued nothing), `breaker:iterations`, `breaker:tokens` or `breaker:cost` (a limit ended the exploration; the run is **not** failed, it writes its report with the cut and the open questions as gaps) are set when exploration ends; `finalized` is the reason of a run whose exploration ended by its own gate (`finish_exploration`) and whose report was accepted. A report that is accepted after a cut keeps the cut as the reason. A breaker that fires in synthesis (the reserve is used up) fails the run, with `stop_reason` and the `error` naming the breaker.
- `steps` counts the turns (model calls), and the limit is `max_iterations` (default 60), not `max_steps`.

The states of an investigative run (`gb_agent_research_states.state`) are the ones above plus: `planning` (the first level of questions is asked for), `selecting` (the loop picks the next question, no model call; detail `frontier_next`), `extracting` (forced extraction: only the notes tools), `verifying` (an UNVERIFIED claim of the current question has no challenge yet), `challenging` (a challenge is open), `consolidating` (the notes are over `notes_max_tokens`) and `synthesizing`. The `detail` of a turn is `turn 7 of 60, question q_003`. A tool call has its usual state (`searching`, `reading`, `using_tool`, ...); `fetch_page` is `reading`.

The loop keeps the prompt of each turn bounded (the task, the notes, the frontier summary and the last `keep_turns` turns); a result above `extraction_threshold` characters forces an extraction turn; and the last turn is forced to call `finalize_report`. The notes file is `notes_<user>_<run>_<notes_id>.md` in `research_notes_dir`; with `research_notes_s3_bucket` set (also `RESEARCH_NOTES_S3_BUCKET`, `research_notes_s3_region`, `research_notes_s3_prefix`, default `investigative_notes`) every write is also put to `s3://<bucket>/<prefix>/<user>/notes_<user>_<run>_<notes_id>.md` (one research bucket for all research agents, a subdirectory per user; a failed put is logged, and a file missing locally is read back from the bucket); if it is missing or differs from the latest stored version, it is rebuilt from `gb_agent_notes_versions` (reason `restore`) before the next turn.

### Reading a run with SQL

Everything the loop did is in plain tables. In the queries `?1` is the run id (`$1` on PostgreSQL); they are tested on SQLite (`TestInvestigativeRunThroughTheManager`).

The frontier as a tree, from `parent_id`:

<!-- query: tree -->
```sql
WITH RECURSIVE tree(id, depth, status, question, path) AS (
    SELECT id, depth, status, question, id FROM gb_agent_frontier_questions WHERE run_id = ?1 AND parent_id IS NULL
    UNION ALL
    SELECT q.id, q.depth, q.status, q.question, tree.path || '/' || q.id
    FROM gb_agent_frontier_questions q JOIN tree ON q.run_id = ?1 AND q.parent_id = tree.id
)
SELECT path, depth, status, question FROM tree ORDER BY path;
```

The history of every question (what was selected, retried, reopened or pruned, by whom and why):

<!-- query: transitions -->
```sql
SELECT question_id, seq, from_status, to_status, trigger, actor, reason, caused_by, created_at
FROM gb_agent_frontier_transitions WHERE run_id = ?1 ORDER BY id;
```

Challenges per target and per strategy, and the distribution of outcomes (a run where every challenge `survived` with one strategy is suspect):

<!-- query: challenges_per_target_and_strategy -->
```sql
SELECT target_type, target_id, strategy, COUNT(*) AS challenges, SUM(outcome = 'survived') AS survived
FROM gb_agent_challenges WHERE run_id = ?1 GROUP BY target_type, target_id, strategy ORDER BY target_type, target_id;
```

<!-- query: outcome_distribution -->
```sql
SELECT outcome, COUNT(*) AS challenges FROM gb_agent_challenges WHERE run_id = ?1 AND status = 'closed' GROUP BY outcome;
```

What changed after a `refuted` or `weakened` challenge (the questions and claims it moved; `caused_by` of a transition holds the challenge id):

<!-- query: effects_of_challenges -->
```sql
SELECT c.id AS challenge, c.outcome, c.result_claim_id, t.question_id, t.from_status, t.to_status, t.reason
FROM gb_agent_challenges c
JOIN gb_agent_frontier_transitions t ON t.run_id = c.run_id AND t.caused_by = c.id
WHERE c.run_id = ?1 AND c.outcome IN ('refuted', 'weakened') ORDER BY t.id;
```

The notes at each write, with the state the run was in (`state_seq` of a version is the `seq` of the state):

<!-- query: notes_at_each_write -->
```sql
SELECT v.seq, v.reason, s.state, s.detail, LENGTH(v.text) AS size
FROM gb_agent_notes_versions v
LEFT JOIN gb_agent_research_states s ON s.run_id = v.run_id AND s.seq = v.state_seq
WHERE v.run_id = ?1 ORDER BY v.seq;
```

How investigative runs ended, over all runs:

<!-- query: runs_by_stop_reason -->
```sql
SELECT stop_reason, status, COUNT(*) AS runs, AVG(cost_usd) AS avg_cost_usd, AVG(steps) AS avg_turns
FROM gb_agent_research_runs WHERE phase <> '' GROUP BY stop_reason, status;
```

The ATIF trajectory of an investigative run has one agent step per model call, with the turn, its kind, the state, the phase and the question id in the step's `extra`, and the phase and stop reason in the trajectory's `result` extra.

## Sub-agents

A research agent can spawn **sub-agents**, other research agents that take sub-tasks (`spawn_subagent`, `wait_subagents`). Every sub-agent is spawned with a reason. A sub-agent is a run like any other: it has its own states, usage and report, and a `parent_id`, a `name` and a `reason` (`null`/`""` for a research a user started); `GET /v1/gb/research?parent_id=` lists the sub-agents of a run. They are bounded (5 per agent, 10 per research, 2 levels deep) and are cancelled when their parent ends. All of it, the tools and the limits, is in [subagents.md](subagents.md).

## How it works

- `internal/agent`: the loop and the research agent ([deep-research-agent.md](../deep-research-agent.md)). It has an `Observer` (before and after each model call and each tool run) that the manager uses to record the states, and `ReportUsage`, with which a tool that calls a model (the web search) adds its tokens and cost to the run.
- `internal/agent_research`: the `Manager` makes a `researcher` for each agent, the struct that holds all of its state (see [isolation](../deep-research-agent.md#isolation)), and starts it in a goroutine of its own (at most 3 working at once per user); the manager creates the run and its first state in one transaction, and the researcher enters a state in a transaction at each event (end the previous state, insert the new one, set the run's `state`), writes the usage after every model call and tool run, and ends the run (its status, report or error, and the terminal state) in a transaction. A failed write of the progress is logged and does not stop the agent. Storage is in `agent_research/db/sqlite` and `agent_research/db/postgres` (each with its schema); the types are in `agent_research/core`.
- `internal/agent_research/investigative.go` and `internal/agent_research/investigate/`: the investigative loop ([above](#the-investigative-loop)): `loop_state.go`, `prompt.go`, `evict.go`, `gates.go` and `breakers.go` are pure; `loop.go` is the effect layer (model, tools, database, notes file); `investigative.go` connects it to the researcher (limits from the definition and the options, the states, the usage, the phase).
- `internal/agent_research/team.go` and `internal/agent/team.go`: the sub-agents; the prompts are in `internal/agent/prompts/` (see [subagents.md](subagents.md)).
- `internal/agent/agent_memory/memory.go`: `Memory` (the remember manager, the database, the LLM and the options of the remember and query endpoints), which `api.Register` gives the manager (`Manager.UseMemory`; `agent_research.Memory` is an alias of it); each agent gets its own view of it (`Memory.Knowledge`: its user, dataset and session) that implements `agent.Knowledge`, the interface behind the tools `remember` and `query` (`internal/agent/knowledge.go`). The package is shared with the standalone binary ([deep-research-agent.md](../deep-research-agent.md#the-standalone-binary-gligabao_agent)). Without a `Memory` the agents only have the web tools.
- `internal/api/research`: the REST handlers; `internal/api/mcp` and `cli` call them (the MCP tools in process, the CLI over HTTP).
- `internal/agent_research/remote.go`: the `modal` runner ([above](#where-the-agent-runs-the-runner)): `Manager.UseRemote` gives the manager the `Remote` it starts the jobs with, and a `remoteRun` follows the job of each such run on a goroutine of its own, writing its events to the run like a `researcher` does, and, when the job ends, downloads its export and has the `Importer` (`Manager.UseImporter`; the server's is in `internal/sys/server/research_remote.go`, over `internal/db/dbimport`) load it.
- When the server stops, the agents are cancelled and their runs are ended as `failed` ("interrupted") before the databases close.

## Not included yet

Following again, after a restart of the server, the jobs it had running in Modal (their export, and so what they learned, is lost with them); the user's own knowledge base *for* the agents that run in Modal (they start with an empty one of the container's; what they learn is merged back into the user's when they end), the import of an export into a server that has no vector store of the same embedding size (the rows are imported, the vectors are not), other places to run the agent than Modal, cancelling a run by a user (only the admin UI can stop one), a stream of its states (poll `GET /v1/gb/research/{id}`), storing the report itself in a dataset, waiting for the queued snippets before a run ends, counting the cost of `remember` and `query` in the run's cost, recording the agent's model calls in `gb_inferences`, and, for the investigative loop, the frontier tree in the admin UI, parallel workers on the frontier and sub-agents sharing the notes. See the [roadmap](../roadmap/ROADMAP.md).
