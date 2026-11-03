# The deep-research agent

The deep-research agent is the part of gligabao that does the research: given a topic, it searches the web, reads pages, checks claims against more than one source and writes a report. This page is about **the agent itself**: its loop, its prompt, its tools (two web tools, and two more for the internal knowledge base), its limits and what it costs. How it is run as a background job (runs, states stored in the database, REST, MCP, CLI, and whether it runs in the server or in Modal) is in [research.md](research.md).

Code: `internal/agent/agent.go` (the generic loop), `internal/agent/research.go` (the research agent and its tools), `internal/agent/prompts/` (all the prompts, one Markdown file each, embedded in the binary: see [subagents.md](subagents.md#prompts)), `internal/agent/knowledge.go` (the `remember` and `query` tools), `internal/agent_memory/memory.go` (how they reach the knowledge base), `internal/ai/websearch.go` (the web search call), `internal/agent_research/manager.go` (starts the jobs) and `internal/agent_research/researcher.go` (the state and goroutine of each agent).

It needs an Anthropic API key (`ANTHROPIC_API_KEY`, or `anthropic_api_key` in `gligabao.toml`); without it a research cannot be started (`503`).

## The loop

`internal/agent` is a plain tool-calling loop, **no framework**. An `Agent` is a model (`ai.Client`), a system prompt, a list of tools and three limits (`Model`, `MaxTokens`, `MaxSteps`). `Run(ctx, prompt)` does this:

1. Send the conversation so far to the model (Anthropic's Messages API, through `ai.Client.Raw`), with the tool list. This is one **step**.
2. Add the model's answer to the conversation. Its text blocks are collected; each `tool_use` block is a tool the model wants.
3. Run every tool the model asked for, one after the other, and send their results back (as `tool_result` blocks, with `is_error` set for a failed one) in the next turn.
4. If the model asked for no tool, stop: the text of that last turn is the answer. Otherwise go back to 1.

Details that matter:

- **The last step cannot use tools.** At step `max_steps` the request carries `tool_choice: none`, so the model must answer with text, and a run always ends with a report (or with an error). The step before it is the last that can search or read, so a run that wants to use all its steps does `max_steps - 1` rounds of research, then writes.
- **A failing tool does not fail the run.** An unknown tool (or one that panics), a bad input, a page that cannot be read, a search that errors: the error text is sent to the model as the tool's result, flagged as an error, and the model decides what to do (try another page, another query, or go on without). Only a failed **model call** ends the run.
- **The conversation grows.** Every answer and tool result stays in the conversation (nothing is summarised or dropped), so later steps cost more input tokens than early ones. This is why the number of steps is capped, and why the page text a tool returns is cut (see `fetch_url`).
- **The report is the text of the last turn** only, trimmed. A run that ends with no text has failed ("the agent ended without a report").
- **An `Observer`** (`BeforeModel`, `AfterModel`, `BeforeTool`, `AfterTool`) is told what the loop does, one event at a time, on the goroutine of the agent. The job manager uses it to record the [states](research.md#runs-and-states) and the cost as the run goes. A tool that calls a model itself reports what it spent with `ReportUsage`, so that it is added to the run's totals.

## Agent definitions

What the agent is is not fixed in code: an [agent definition](agent-defs.md) (a TOML or JSON file in `agent_defs/` next to the binary, loaded at startup) sets its prompt, model, steps, tools, search preferences and domain filters, knowledge-base access, cost caps, skills, and when and which sub-agents it starts. `agent.FromDef` builds the `Agent` of a definition; `agent.ResearchAgent` is the built-in `default` one, which is what this page describes. A research picks a definition with `agent`; each sub-agent is built from the definition of its role. `Agent.OutOfBudget` is the hook of the cost caps: when it says the budget is spent, the next model call is the last one.

## Isolation

Every research agent is **isolated** from the others, so that one cannot slow down, confuse or break another:

- **One goroutine each.** The `Manager` starts every agent on a goroutine of its own and does not touch it afterwards (it only counts the agents of each user, to enforce the limit of 3 working at once).
- **One master struct each.** All the state of an agent is in a single `researcher` struct (`internal/agent_research/researcher.go`): its run record, options, the `agent.Agent` it runs, the state it is in, its running totals and its HTTP client. Only its own goroutine uses it, so it needs no lock. Inside, `agent.Agent.Run` keeps the conversation, the totals and what the tools spent in a `loop` struct made for that call, and the `Agent` is only read: even calls of the same `Agent` value at the same time share nothing.
- **Its own page reader.** `fetch_url` uses an HTTP client of the agent (`agent.NewHTTPClient`: its own transport and connections, no cookies), closed when the agent ends, not the process-wide default client.
- **What is shared** is only what is meant to be: the database handle (and the store functions) and the LLM client, which are safe for concurrent use. The agents write to their own rows only.

## The research agent

`agent.ResearchAgent` configures the loop for research:

| Setting | Value |
| ------- | ----- |
| Steps (`MaxSteps`) | 20 by default; the job lets the caller choose 1 to 40 (`max_steps`) |
| Answer size (`MaxTokens`) | 8192 tokens per model call (so a report is at most that long) |
| Model | the server's default model, or the `model` of the request. It runs the loop and writes the report |
| Tools | `web_search` and `fetch_url`; with a knowledge base (the case when it runs as a job on the server), also `query` and `remember`; as a job, also `spawn_subagent` and `wait_subagents` (unless it is at the deepest level) (see [subagents.md](subagents.md)) |

The system prompts live in Markdown files in `internal/agent/prompts/` (embedded in the binary; the parts and who gets them are in [subagents.md](subagents.md#prompts)). The base prompt tells it to: research the topic by searching the web several times with different queries; read the most relevant pages with `fetch_url`; check claims against more than one source; stop when it can answer well; and then write a report: **a short summary first, then the details, then the list of the source URLs used**, saying what it could not find out and never guessing. The user turn is the topic as given.

When the agent has a knowledge base, the prompt adds how to use it: `query` it, besides the web search and the page reads, to check what is known already before searching the web for it and to check what was found against it; and, while searching and reading, `remember` the important parts (short self-contained snippets with their context, never whole pages) with the URL as the source. It is told that `remember` does not block by default and that it must use `sync=true` only when it needs the text absorbed before it goes on.

How many searches and pages it uses, and when it stops, is the model's decision; the prompt and `max_steps` are all that steer it. A narrow question usually ends well before the last step; a broad topic uses them all.

### Tool `web_search`

Input: `{"query": "..."}`. It is **not** a bare search engine: it makes a separate call to Anthropic's cloud-based web search tool (`web_search_20250305`, a server tool: Anthropic runs the searches and reads the pages; see [web-search.md](web-search.md)). That call has its own small prompt, asking for what was found in **at most 300 words, with the facts and dates**, at most 1024 output tokens, and **at most 2 searches** for the query. The model of the loop sees only the result: that summary, then `Sources:` with one line per page (`- <url> (<title>)`).

So the agent never sees raw search results, only a short briefing and the URLs, which it can then open with `fetch_url`. The call runs on the client's default model, which must support the web search tool (this is independent of the `model` of the agent). Its tokens and cost go into the run's totals through `ReportUsage`; every search is billed on top of the tokens ($0.01 each, `ai.WebSearchRequestUSD`).

### Tool `fetch_url`

Input: `{"url": "..."}`. It downloads a page with a plain `GET` (Go's default HTTP client: redirects are followed, no cookies, no JavaScript) and returns its text:

- The URL must start with `http://` or `https://`; anything else is an error.
- A status other than `200` is an error (`<url>: status 404`); the model is told and goes on.
- At most 1 MiB is downloaded, and the HTML is turned into text **crudely on purpose**: `script`, `style` and `noscript` blocks and all tags are removed and white space is collapsed. There is no content-type check (a PDF comes back as garbage) and no readability extraction.
- The model gets **the first 15,000 characters** of the text. The rest of a long page is lost to it.

`fetch_url` fetches whatever URL the model chooses, from the machine gligabao runs on, with no allow-list: a page on a private network is reachable if the server can reach it. Keep that in mind where the topic comes from untrusted users (see [Hooks](hooks.md#auth) for who can start a research).

### Tool `query`

Input: `{"question": "..."}`. It asks the **internal knowledge base** (the memory of gligabao, see [query](query.md)) of the user who started the run, in the dataset of the run (`main` unless the run asked for another one): the same pipeline as `POST /v1/gb/query` and the same options (vectors, geo), run in process. The model gets the answer, then `Facts it is based on:` with one line per fact (`source relation target: description`, at most 15, then `... and N more`). A dataset that does not exist yet (nothing was ever remembered into it) is not an error: the tool says that the knowledge base is empty. Any other failure is an error text sent to the model like the failure of any tool.

The query calls the LLM of the server, but that is not added to the totals of the run (see [What a run costs](#what-a-run-costs)): it is recorded as an inference (`gb_inferences`) and in the query run log (`gb_query_runs`) like any other query, with the session `research-<run id>`.

### Tool `remember`

Input: `{"text": "...", "source": "...", "sync": false}`.

- `text` (required, at most 20,000 characters): the snippet to store: a fact, figure, date or quote with its context. Longer text is an error that tells the model to remember the important parts only, or in several calls.
- `source` (optional): where it comes from, normally the URL. It is put in front of the text (`Source: <url>`, a blank line, the text), so that it is part of what the knowledge base absorbs.
- `sync` (optional, default `false`): **whether the call blocks**. The text is submitted to the [remember manager](remember-manager.md) as a request of the user, like `POST /v1/gb/remember` does (kind `knowledge`, the same options, session `research-<run id>`), and then:
  - `sync=false` (the default): the call returns as soon as the text is **queued**, with the ID of its remember run (`queued (remember run 12); ...`), and the agent goes on with the search. The text is absorbed in the background; the run is not tied to the tool call (it ends only when the server stops), so it also completes if the agent has ended by then. If it fails later nobody is told: it is in the log and in the run log of the remember pipeline (`gb_remember_runs`).
  - `sync=true`: the call **blocks** until the run is over, and returns what was stored (`remembered (remember run 12): 3 entities and 5 facts in the knowledge base`) or its error. Use it only when the agent needs the text in the knowledge base before it goes on, for example to `query` for it right away.

The remember manager runs one run at a time, in order, so the snippets of all the users and agents wait in one queue: a `sync=true` call waits for the ones submitted before it, and text queued without `sync` may not be found by `query` yet when the agent asks. That is why blocking is the exception, not the rule: most snippets are only stored for later, and the agent should not wait for them.

Without a knowledge base (an agent built with `agent.Research`, or a server with no LLM for the memory) the agent has neither tool, and the prompt does not mention them.

## Sub-agents

As a job, the agent can hand sub-tasks to other agents with `spawn_subagent` and `wait_subagents`. Every spawn carries a reason, and the prompt of the agent that can spawn says it should do so only when really warranted. The tools reach the job through `agent.Team` (two methods, `Spawn` and `Wait`) and are added to the agent with `agent.AddTeamTools`. Everything about them (the tools, the limits, what a sub-agent costs) is in [subagents.md](subagents.md).

## What a run costs

A run's cost is the sum of:

- the **model calls of the loop**, one per step (up to `max_steps`), each with the whole conversation as input, priced per token by the model's price table;
- the **model calls inside `web_search`**, in tokens, plus **$0.01 per search** (up to 2 per `web_search` call).

The `remember` and `query` tools also call the LLM (extraction, answering), but what they cost is recorded as inferences of their own (`gb_inferences`) and is **not** added to the run's `cost_usd`; a `remember` that does not wait also runs after the tool returned. Remembering many snippets therefore costs more than the run shows.

The worst case is therefore bounded by `max_steps` and by the number of tool calls a step makes (the model may ask for several tools in one step), but the cost is only capped when a [cost cap](agent-defs.md#limits-cost-caps) is set (in the agent definition, the request or the server's `research_max_cost_usd`; the built-in agent has none). The run's `input_tokens`, `output_tokens` and `cost_usd` are updated after every model call and every tool run, so the cost can be followed while it works (a model with no known price counts $0 for its tokens, but the searches are still counted).

## Using the agent from Go

The agent is internal to the module; the supported way to run it is the job ([research.md](research.md)). In the module, `agent.Research(ctx, client, question)` runs it once and returns a `Result` (`Answer`, `Steps`, `ToolCalls`, `InputTokens`, `OutputTokens`, `CostUSD`); `agent.ResearchAgent(client, httpClient, kb)` (`httpClient` from `agent.NewHTTPClient()`, or `nil`; `kb` an `agent.Knowledge`, or `nil` for an agent without the knowledge tools) returns the `Agent` to change the model, the limits, the tools, the observer or the [trajectory](#the-trajectory-atif) recording before calling `Run`. `agent.Knowledge` is two methods, `Remember(ctx, text, wait)` and `Query(ctx, question)`; the job supplies one over the user's dataset (`agent_memory.Memory.Knowledge`; `agent_research.Memory` is an alias of `agent_memory.Memory`).

## The standalone binary (`gligabao_agent`)

Besides the job on the server, the agent has a deployment of its own: `build/gligabao_agent`, built by `./run_build.sh` next to `build/gligabao`. It is the research of the server without the server, for places that must stay small (a Lambda, a bare container): no HTTP API, no admin UI, no training, no evals. It has what a research job has: the [research manager](research.md) (`internal/agent_research`) runs the agent and its [sub-agents](subagents.md), and records every run and state in the database; and the knowledge base, in process. Code: `cmd/gligabao_agent/` (`main.go`: flags and output; `app.go`: the databases, the managers and the research).

```
ANTHROPIC_API_KEY=... OPENAI_API_KEY=... gligabao_agent [flags] TOPIC | -
```

**How it runs.** It opens the databases, builds the remember manager, the knowledge base (`agent_memory.Memory`, the same code the server gives its research agents) and the research manager with `UseMemory`, starts one research (`Manager.Start`, with the options a `POST /v1/gb/research` has: topic, `max_steps`, `model`, `dataset`), waits for it and its sub-agents to end, and prints the report of the run. So the agent has all the tools of a job (`web_search`, `fetch_url`, `query`, `remember`, and `spawn_subagent` and `wait_subagents`), the same prompts, limits (up to 10 sub-agents, 2 levels deep) and states as in [research.md](research.md#runs-and-states). The run, its states, its sub-agents, the usage and the report are in `gb_agent_research_runs` and `gb_agent_research_states` as for a job (`gb_remember_runs` and the rest of the knowledge base tables too), and what the agents remember carries the session `research-<run id>`.

**The databases.** `internal/db` is opened with the knowledge base tables (relational and graph, and the vector store) and the research run tables only; the tables of training, evals and training audits are not created (`internal/db/dbfull` adds them for the server).

- **SQLite** (the default): the file is `--db`, else `$GLIGABAO_DB`, else `build/gligabao.db` (the server's default, so by default the agent shares the server's knowledge base and shows in its research views). The chromem vector store (`<name>.vectors/`) and the image directory (`<name>.images`) are next to it, or `--vector-dir`. Missing files are created. On a Lambda point it to a writable path (`/tmp/...`) or a mounted volume.
- **PostgreSQL / Pinecone**: with `--config gligabao.toml` (`db_backend = "postgres"` and `postgres_dsn` or `POSTGRES_DSN`, `vector_store = "pinecone"` and the Pinecone settings). The config file is the same as the server's (the settings the binary does not use are accepted and ignored); without `--config` the settings are the defaults and the environment. Several runs of the binary (and a server) can share one PostgreSQL database: unlike the server, the binary does **not** mark the running runs it finds as failed when it starts (`agent_research.KeepActiveRuns`), because they are the other processes' runs. A run of the binary that is killed hard (not by SIGINT/SIGTERM, which end it properly) therefore stays `running` in the database until something else marks it.
- Keys: `ANTHROPIC_API_KEY` (the agents, the extraction of `remember`, the answers of `query`) and `OPENAI_API_KEY` (the embeddings, required by the knowledge base), from the environment or the config file.
- `--user` (default `agent`) and `--dataset` (default `main`) choose whose knowledge base, and which dataset of it, the agents query and remember into (the user of the run, and the `dataset` of a research). The limit of 3 agents working at once per user applies per process.
- **`remember` without `sync` does not get lost**: the process is one-shot, so after the research ended it waits until the text that was only queued is absorbed (`Memory.Drain`), then stops the managers and closes the databases. A timeout or SIGINT/SIGTERM cuts that wait short (the text not absorbed yet is lost, and the run log of the remember pipeline says so).

**Using it.**

- The topic is the argument (at most 2000 characters), or stdin if it is `-`.
- The **report goes to stdout**; the totals and everything else go to stderr, so `gligabao_agent "..." > report.md` works. The totals (steps, tool calls, tokens, cost) are those of the **whole tree**: the agent and its sub-agents (on the server each run has its own only). As in the job, they do not include what `remember` and `query` spend.
- Flags: `--agent NAME` (the [agent definition](agent-defs.md), default `default`), `--agent-defs-dir DIR` (default: `agent_defs` next to the binary), `--agent-bundle FILE` (definitions as JSON instead of a directory: what the server sends to Modal), `--max-cost-usd N` (a cap on the whole research), `--max-steps` (1 to 40, default: the agent's, 20 for `default`), `--model` (default: the built-in default model; it must support the web search tool), `--timeout` (a duration like `10m`, default none), `--json` (stdout is `{"report", "run_id", "subagents", "steps", "tool_calls", "input_tokens", "output_tokens", "cost_usd"}`), `--verbose` (writes every state the agents enter, the sub-agents' too, and the log of the databases and managers, to stderr), `--events` (the progress as JSON lines on stderr, see [In Modal](#in-modal-the-modal-runner); not together with `--verbose`), `--export FILE` (write the state to an archive, see [the export](#the-export-gligabao_agent---export)), `--atif` (add the run's [ATIF trajectory](#the-trajectory-atif) to the `--json` output as `trajectory`; needs `--json`, or `--atif-dir`), `--atif-dir DIR` (store the trajectory as `<trajectory_id>.json` in that directory, also when the research failed), `--config`, `--db`, `--vector-dir`, `--user`, `--dataset`, `--version`.
- Exit codes: `0` ok, `1` the research did not complete (the run failed or was stopped: a model call failed, the timeout was reached, SIGINT/SIGTERM), `2` bad flags, no topic, a missing API key or a database that cannot be opened.

**Small by construction.** It links `internal/agent`, `internal/agent_research`, `internal/agent_memory`, the memory packages and the databases they need (both SQLite, in pure Go, and PostgreSQL, so the backend is chosen at run time), `internal/ai` and the config. `TestMinimalDependencies` in `cmd/gligabao_agent` fails if the HTTP API, the server, training, evals, `internal/db/dbfull`, `internal/db/dbimport` (the server loads exports; the binary only writes them), the CLI or the admin UI get linked. It is built with `CGO_ENABLED=0`, `-trimpath` and `-s -w`, so it is a static binary.

### In Modal (the `modal` runner)

The binary is also what runs a research **in Modal** ([research.md](research.md#where-the-agent-runs-the-runner)): the Modal app of the [sidecar](training/sidecar.md#the-research-agent-in-modal) bakes a Linux x86-64 build of it (`build/gligabao_agent_linux_amd64`, made by `./run_build.sh`) into the image of its `run_agent` function, and the function runs it with `--json --events` on a temporary SQLite database, the topic on stdin. `--events` is the protocol between the two: it makes the binary write its progress to stderr as one JSON object per line, and leaves the report to the `--json` output on stdout:

- `{"event":"state","agent":"main"|"<sub-agent name>","state_seq":N,"state":"searching","step":2,"detail":"...","error":""}` each time an agent enters a state (the states of [research.md](research.md#runs-and-states)); `state_seq` numbers the states of that agent from 1. When a state that was reported ended with an error (a page that could not be read), the line is written once more, with the same `agent` and `state_seq` and the `error`.
- `{"event":"usage","steps":N,"tool_calls":N,"input_tokens":N,"output_tokens":N,"cost_usd":0.12,"subagents":N}` whenever the totals of the whole tree of agents change.

The lines come from the same poll of the database as `--verbose` (every half second), so a state shorter than that may be reported only once it has ended. Anything else on stderr (warnings, the totals) is not an event.

### The trajectory (ATIF)

The agent can record a run as an **ATIF trajectory** (the format the [evals](evals/knowledge.md) use for `remember` and `query`). It is opt-in: nothing is recorded unless it is asked for.

- **One agent** (`internal/agent`): set `Agent.Trajectory` and `Run` returns the trajectory in `Result.Trajectory`. It has the system prompt and the prompt, then one agent step per model call with its text, the tool calls it asked for (with the model's call IDs and arguments), their results, the model's tokens, cost and latency, and what the tools themselves spent (`tools_usage`, the failed calls in `failed_tool_calls`, the time of each in `tool_latency_ms`). The run's totals (the tools' spending included) and its answer are in `extra.result`. A run that fails also gets a trajectory (returned with the error, the error in its `notes`). `Agent.TrajectoryDir` also stores it as `<trajectory_id>.json` there (`Result.TrajectoryPath`); empty (the default) only returns it, so a Lambda writes nothing. `Agent.Name` names the agent in it (`gligabao-research` for the research agent) and `Agent.SessionID` sets the session ID. Code: `internal/agent/trace.go`.
- **A research with sub-agents** (`internal/agent_research/trajectory.go`): `Manager.UseTrajectories(fn)` makes the agents of the researches started from then on record trajectories. When a research ends, `fn(runID, trajectory)` gets **one** trajectory, the main agent's, that holds the steps of every sub-agent at every depth (each sub-agent's own sub-agents are merged into it first). The steps are in the order they started and numbered again, the final metrics are those of all of them, and each step says in its `extra` which agent took it (`agent`, `"main"` or the sub-agent's name; `run_id`, `parent_run_id`, `depth`). `extra.agents` lists the agents with their status, steps, tool calls, tokens and cost. It is given also for a research that failed or was stopped. The session ID is `research-<run id>`. The server, its REST API and the MCP tools do not use it yet.
- **In `gligabao_agent`**: `--atif` adds that trajectory to the `--json` output (`trajectory`) and `--atif-dir DIR` stores it as `<trajectory_id>.json`, also when the research failed. `--export` includes it in the archive (`extra/trajectory.json`).

### The export (`gligabao_agent --export`)

`--export FILE` is how what the agent learned leaves a place whose storage is thrown away (the Modal container). After the research (also if it failed or was stopped: what it learned until then is worth keeping) the binary waits for the agents and the queued `remember` calls and writes **one ZIP archive** with the whole SQLite database (the knowledge base, the runs of the agent and its sub-agents with their states and reports, the model calls and their costs, the remember and query runs), every embedding of the Chromem vector store with its metadata, the image files and the ATIF trajectory (`extra/trajectory.json`). With PostgreSQL or Pinecone nothing is exported for them (those servers keep their data; the manifest and stderr say so). `--json` has an `export` object (path, bytes, tables, rows, vectors, images, skipped); a failed export makes the binary exit with `1` after it printed the report. The format (JSON lines per table and per vector dataset, a manifest with the columns, keys and foreign keys) is in the documentation of `internal/db/dbexport`.

The Modal function passes `--export` and hands the file to the server, which loads it into its databases (`internal/db/dbimport`): see [research.md](research.md#what-comes-back-the-import).

To run it on another platform cross-compile it as any Go program, e.g. `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bootstrap ./cmd/gligabao_agent`. Without `./run_build.sh` the version it reports is `dev` (the script sets it from `VERSION` with `-X main.version`).

## Not included yet

Cancelling a run, counting the cost of `remember` and `query` in the cost caps, waiting for the snippets still queued before a run ends, storing the report itself in the knowledge base, summarising or dropping old turns of a long conversation, a better page reader (PDF, readability), blocking private addresses in `fetch_url`, other search providers than Anthropic's, and recording the agent's model calls in `gb_inferences`. See the [roadmap](roadmap/ROADMAP.md).
