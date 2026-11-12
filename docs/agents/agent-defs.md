# Agent definitions

A [research agent](research.md) is described by a **definition**: a TOML or JSON file that says what the agent is (its name, description, version and system prompt), how it works (model, steps, tools, search and knowledge-base settings), what it may spend (cost caps), which skills it has, and which loop it runs (with how many workers, for the investigative one). The files are written by hand, versioned with git next to the binary, and loaded once when the server starts. A research names the definition it wants (`"agent": "news_scanner"`); without a name it runs the built-in `default`, which is today's research agent.

This makes it easy to have several agents that search different areas, behave in a certain way and have preferences, with no change to the Go code.

Code: `internal/agent/agent_defs` (the format, the loader, the checks, the registry, the bundle), `internal/agent/research.go` (`FromDef` builds the agent of a definition), `internal/agent_research` (the manager uses the definition of every agent it starts, the workers of the investigative loop included). Examples: `build/agent_defs/`.

## Where the files are

- The directory **`agent_defs/` next to the binary** (`gligabao` and `gligabao_agent`). `agent_defs_dir` in `gligabao.toml` or `serve --agent-defs-dir` (and `gligabao_agent --agent-defs-dir`) say another one; a relative path in the config file is relative to the file.
- **One agent per file**, `*.toml` or `*.json`, directly in the directory. The subdirectories hold what the files refer to: prompt files (any subdirectory, by convention `prompts/`) and skills (`skills/`).
- A directory that does not exist is fine: only `default` is there.
- The definitions are read **once, at startup**. A file that is not valid stops the server, with every problem of every file in one error (file, key, what is wrong): an agent that silently does not load is worse than a server that does not start. Change a file, restart the server.

```
agent_defs/
  default.toml              # optional: replaces the built-in default agent (a warning is logged)
  news_scanner.toml
  regional_scout.toml
  prompts/news_scanner.md
  skills/citation_style.md
```

## The file

```toml
schema = 1                       # the format version of the file (1)
name = "news_scanner"            # unique; 1 to 40 lowercase letters, digits, - or _
version = "1.2.0"                # the agent's own semantic version (metadata: see "Versions")
description = "Scans recent news in a topic area and reports with dates and sources."

system_prompt_file = "prompts/news_scanner.md"   # or system_prompt = """...""": exactly one of them

model = ""                       # "" inherits: see "Model"
max_steps = 25                   # 1 to 40, default 20
max_tokens = 8192                # of each answer, default 8192
loop = "simple"                  # "simple" (default) or "investigative": see [investigate]
tools = ["web_search", "fetch_url"]   # default: both; query and remember are NOT listed here: see [knowledge]

skills = ["citation_style", "dates"]  # agent_defs/skills/citation_style.md, ...; a missing file is a warning, the agent starts without it
required_skills = []                  # the same, but a missing file is a startup error

[search]    # how it searches the web
[knowledge] # how it uses the knowledge base
[limits]    # cost caps
[investigate] # knobs of the investigative loop (only with loop = "investigative")
[code]      # the run_code tool, Python in a sandbox (only with loop = "investigative", optional)
[schedule]  # runs by itself on a CRON schedule (optional)
```

Unknown keys are errors. Every field but `name`, `version`, `description` and the prompt is optional.

### The system prompt

`system_prompt` (or the text of `system_prompt_file`, a path relative to the file, inside the directory) is the **base** of the agent's prompt: it replaces the built-in research prompt. The runtime adds, in this order, what the tools need to be told: the search preferences, the part for the knowledge base (if it has the tools), and last the skills. (A worker of the investigative loop gets the prompt of a worker in place of the base.) So a prompt file is only about the agent's job and style.

### Skills

`skills = ["citation_style"]` means the file `agent_defs/skills/citation_style.md`. A skill name is lowercase letters, digits, `-` and `_` (no path, no `.md`). A skill file is plain Markdown; it is appended to the agent's system prompt as `## Skill: <name>`, in the order of the file (the required ones first). It is read once at startup. One file can be shared by many definitions. A file that is a link out of the directory is refused.

### `[search]`: web preferences

```toml
[search]
allowed_domains   = []                       # HARD: only these sites (web_search and fetch_url)
blocked_domains   = ["pinterest.com"]        # HARD: never these (not together with allowed_domains)
preferred_domains = ["reuters.com", "apnews.com"]   # SOFT: told to the agent
recency  = "week"          # any (default) | day | week | month | year: SOFT
language = "en"            # SOFT: the language to search in first
location = { country = "DE", region = "Bavaria", city = "Munich", timezone = "Europe/Berlin" }   # web_search's user_location
instructions = "Prefer primary sources and official statistics over commentary."   # told to the agent and to its web_search calls
max_uses_per_call = 2      # searches per web_search call (default 2, at most 10)
max_calls = 0              # most web_search calls in a run (0: no limit)

[search.fetch]             # fetch_url
allowed_domains = []
blocked_domains = []
max_page_chars = 15000     # how much of a page the model gets (1000 to 100000)
max_calls = 0              # most fetch_url calls in a run
```

- **Hard** settings are enforced. `web_search` sends the domain lists and the location to Anthropic's search tool (which takes `allowed_domains` or `blocked_domains`, not both, so the loader rejects a file that sets both). `fetch_url` checks the host of every URL, and of every redirect, itself: a host matches a listed one or any subdomain of it. A refused call is an error the model reads and goes on from.
- **Soft** settings (`preferred_domains`, `recency`, `language`, `instructions`) are text in the prompts; nothing enforces them (the search tool has no recency or language filter).
- A call over `max_calls` is refused with an error that tells the agent to write its report with what it has.
- The settings belong to the agent; a worker of the investigative loop has those of its supervisor's definition.

### `[knowledge]`: the knowledge base

```toml
[knowledge]
access   = "read_write"    # read_write (default) | read_only | none: decides the tools query and remember
dataset  = ""              # the dataset used when the request names none ("" is "main")
dataset_locked = false     # true: a request that names another dataset is refused (400)
query_first = true         # tell the agent to ask the knowledge base before the web
instructions = "Remember only dated, sourced facts; one fact per snippet."   # added to its prompt
```

- `access` is the one place that gives the knowledge-base tools; listing `query` or `remember` in `tools` is an error. `read_only` gives `query` only (and a prompt that says it cannot add); `none` gives neither. The tools exist only when the server gives its agents a knowledge base at all.
- Dataset: a locked dataset of the definition, else the one of the request, else the default of the definition, else `main`. A worker of the investigative loop works in its supervisor's dataset (so they share what they remember).
- With the `modal` runner the container's knowledge base starts empty, so `read_only` is of little use there (the server logs a warning).

### `[limits]`: cost caps

```toml
[limits]
max_cost_usd      = 1.50   # this agent alone: its model calls and its tools' spend (web search). 0: none
max_tree_cost_usd = 5.00   # the whole research, the sub-agents included. Only the definition of the root agent counts. 0: none
```

A cap is a **soft stop**, not a failure:

- Before each model call the agent checks its own cost against `max_cost_usd`, and the cost of the whole research against the cap of the research. When one is reached, **that call is the last**: the agent is told why and may not call tools, so it writes its report from what it has. The run ends `completed`. Its `writing_report` state says the call was made early. A call that is running when the cap is reached still adds its cost, so the cap can be overshot a little.
- When the cap of the research is reached, **every agent of it stops the same way** at its next model call, and no more sub-agents can be started (the model is told).
- **The cut is said in the report.** The report of an agent that a cap stopped ends with a line, e.g. `*Stopped early: the cost cap of this research ($5.00) was reached, so this report is based on what had been gathered until then and may be incomplete.*` (or, for the agent's own cap, `this agent's cost cap`). The root's report says it also when the cap stopped only some of its sub-agents. A parent reads the sub-agents' notes in what `wait_subagents` gives it.
- The **cap of the research** is the lowest of: `max_tree_cost_usd` of the root's definition, `max_cost_usd` of the request (`POST /v1/gb/research`, MCP, `research start --max-cost`, `gligabao_agent --max-cost-usd`), and `research_max_cost_usd` in `gligabao.toml` (the server's ceiling). A request or a definition can only lower what is above it. The built-in `default` agent has no caps of its own.
- The cost is what the run records (`cost_usd`): it comes from the price table, so a model with no price counts $0 and its caps cannot work. The `remember` and `query` calls are not in it ([deep-research-agent.md](../deep-research-agent.md#what-a-run-costs)).
- In Modal the agent in the container enforces the caps itself (the definition and `--max-cost-usd` travel with the job). As a backstop the server ends a job that reports a cost 25% over the cap of the research; the run is then `stopped` ("stopped by the cost cap").

### `loop` and `[investigate]`: the investigative loop

The server has a built-in investigative agent, `investigator` (next to the built-in `default`): the loop with `web_search`, `fetch_url`, read/write knowledge and a cap of 2 USD. A file called `investigator.toml` replaces it. The admin UI starts it for its "investigative" choice.

`loop = "simple"` (the default, also when the key is left out) is the loop an agent has always had: it searches and reads until it writes the report, in at most `max_steps` model calls. `loop = "investigative"` runs the [deep-research loop](deep-research-loop.md) instead: a frontier of questions with notes, claims and challenges, an exploration phase ended by a gate and a synthesis phase that writes the report without searching (see [the run](research.md#the-investigative-loop)). It has the same tools (`fetch_url` is called `fetch_page` for the model), the same prompt (plus the loop's instructions for each phase), skills, search and knowledge settings and cost caps (`max_cost_usd` is a circuit-breaker there). It runs independent questions in sub-agents when `max_parallel` is above 1 ([subagents.md](subagents.md)). `runner = "modal"` is an error for a research with it. `max_steps` does not apply; `max_iterations` replaces it.

The `[investigate]` table sets the knobs; every one is optional (0 or left out is the default), and the table is an **error** with the simple loop (not a silent no-op). A research can override them with the `investigate` option of the request, which is also an error for a simple agent.

```toml
loop = "investigative"
[investigate]
max_iterations = 60              # turns (model calls) of the run, 2 to 300; the last 15% are the synthesis reserve
max_total_tokens = 0             # input + output tokens of the run, 0 = no limit (also split by the reserve)
synthesis_reserve = 0.15         # 0 to 0.5: the part of the iteration, token and cost limits that exploration cannot spend
max_depth = 3                    # 1 to 6: how deep the question tree goes
max_attempts = 4                 # query formulations on a question before it is unresolvable
max_questions = 40               # questions in a run
keep_turns = 2                   # recent turns kept in the prompt, 1 to 5
extraction_threshold = 4000      # characters of a tool result from which the next turn is a forced extraction
notes_max_tokens = 6000          # the notes over this size make a consolidation turn
min_sources = 5                  # exploration gate: distinct sources in the notes
min_domains = 3                  #   ... from at least this many domains
min_independent_sources = 2      # independent supporting domains a claim needs for VERIFIED
min_challenges = 1               # survived challenges a claim needs for VERIFIED
max_challenges_per_target = 3
gate_priority = 0.5              # a pending actionable question from this priority keeps the gate closed
max_reopens = 1                  # times the synthesis may send the run back to exploration
max_parallel = 1                 # 1 to 8: workers (sub-agents) at once on independent questions; 1 = none (docs/agents/subagents.md)
worker_max_steps = 12            # model calls of a worker, 1 to 40
worker_context_tokens = 1500     # size of the slice of the notes in a worker's brief, 100 to 8000
max_workers = 20                 # workers in the whole run, 1 to 40
min_priority_to_delegate = 0     # 0 to 1: a question below this priority is not delegated
```

The cost cap of the run is `[limits] max_cost_usd` (and the request's and the server's caps, the lowest counts). When any breaker fires in exploration the run goes on to synthesis with a note of the cut and does not fail; if one fires in synthesis the run fails and names the breaker. The knobs and their meaning are in [the design](deep-research-loop.md#configuration).

### `[code]`: the `run_code` tool

An agent with `loop = "investigative"` can have one more tool, `run_code`: a short Python program run in a sandbox ([Pydantic Monty](https://github.com/pydantic/monty), a sandboxed subset of Python in a sidecar process; the design is in [monty-code-execution.md](monty-code-execution.md)). It is for exact arithmetic, unit conversions, counting, parsing a long text and cross-checking figures; only what the program prints and the value of its last expression come back. It is offered in the exploration and planning turns, not in extraction, consolidation and synthesis. The workers (sub-agents) of the loop get it too (without the host functions that read the supervisor's state). A run is stateless, cannot change the frontier, the notes or the claims, and can only call the host functions in the registry (`notes_read`, `frontier_get`, `frontier_list`, `claims_list`, `fetch_page`, `web_search`, `kb_query`, `log`; the fetch ones run the agent's own tools, so their limits, domain rules and cost apply).

```toml
loop = "investigative"
[code]
enabled = true             # required: the table without enabled = true is an error
timeout_s = 10             # wall time of one call, 1 to 60
max_memory_mb = 128        # 8 to 1024
max_recursion = 500
max_output_chars = 4000    # what the model reads back, 200 to 20000
max_calls = 30             # run_code calls per run, 0 = no limit
max_host_calls = 20        # host function calls per run_code call
max_result_chars = 20000   # one host function result
functions = []             # names allowed; empty = all (a name not in the registry is an error)
type_check = false         # Monty's type checker, against the stubs of the functions, before running
```

The table is an **error** with the simple loop. Without the table the agent has no code tool and nothing changes. The sandbox must be installed on the server (the Docker image has it; otherwise `pip install pydantic-monty` and set `monty_python` / `monty_bin` of `gligabao.toml`, or `GLIGABAO_MONTY_PYTHON` / `MONTY_BIN`): a run of an agent with `[code] enabled` fails at its start, naming the missing piece, when it is not there. `build/agent_defs/numeric_investigator.toml` is an example.

### Sub-agents: the `max_parallel` knob (and the removed `[spawn]`)

Sub-agents are a capability of the investigative loop: with `max_parallel` above 1 (and `worker_max_steps`, `worker_context_tokens`, `max_workers`, `min_priority_to_delegate`, in `[investigate]`, above) the loop runs independent questions of its frontier in workers and reduces their findings; the model has no tool to start one, and a definition has no list of kinds of sub-agents. All of it is in [subagents.md](subagents.md).

The `[spawn]` table of earlier versions (the model-driven `spawn_subagent` and `wait_subagents`, the roles with their `on_demand` and `on_start` triggers, `max_depth`, `max_per_agent` and `max_per_tree`) was **removed in 0.189.0**. A file that still has it does not load: `spawn: sub-agents of the simple loop were removed; set loop = "investigative" and investigate.max_parallel to run questions in workers`. A bundle (the JSON of a definition) with a `spawn` field is refused too.

### `[schedule]`: running on a schedule

An agent that should run by itself (a morning briefing, a weekly scan) gets a `[schedule]`. The server then starts a research with this agent every time the CRON expression matches, as if someone had called `POST /v1/gb/research`.

```toml
[schedule]
cron     = "30 7 * * 1-5"                        # minute hour day-of-month month day-of-week; required
timezone = "Europe/Berlin"                       # IANA zone the expression is read in (default UTC)
topic    = "The most important news of {{.Date}}"   # required: what every scheduled run researches
user     = "scheduler"                           # whose runs (and knowledge base) they are (default "scheduler")
paused   = false                                 # true: keep the section in the file without running it
```

- **`cron`** has five fields, as usual: `*`, numbers, ranges (`1-5`), lists (`1,3,5`), steps (`*/15`, `10-40/10`), month names (`jan`..`dec`), day names (`sun`..`sat`; `0` and `7` are Sunday) and the macros `@hourly`, `@daily` (`@midnight`), `@weekly`, `@monthly`, `@yearly` (`@annually`). When both the day of month and the day of week are restricted, a day that matches either counts (like `cron`). There is no seconds field.
- **`topic`** is a Go template with `{{.Date}}` (`2030-01-07`) and `{{.Time}}` (RFC 3339), both of the moment the run was due, in `timezone`. A run is made as the `user`, like a request with that `gb-user-id`: it counts for the user's limit of working agents, writes to the knowledge base of that user (`[knowledge]` decides how) and is listed for that user. The run uses the model, steps, caps and sub-agents of the definition.
- **At most one run per agent at a time.** If the previous scheduled run of the agent still works when the next one is due, the new one is skipped (a warning is logged). The times the server was down are not caught up: after a start the next time after it is used.
- **At least 5 minutes between two runs** (every run is paid for): a schedule that runs more often is a startup error, and so are an expression that never matches (`0 0 31 2 *`), an unknown time zone and a missing `cron` or `topic`.
- **One server** should run the schedules. Every process that serves the same definitions starts its own runs, so with several servers behind one database keep the `[schedule]` sections in one of them only (or `paused`).
- Only the server runs schedules: `gligabao_agent` ignores the section, and an agent that is only used as a worker is never started by it. The schedule is not part of `agent_hash` (when an agent runs is not what it is), so changing it does not make a new hash. It travels in a bundle but the container does nothing with it.
- `GET /v1/gb/research/agents` shows it: `schedule` with `cron`, `timezone`, `topic`, `user`, `paused` and `next_run`.
- The runs are ordinary researches (`agent_name` says which agent, `GET /v1/gb/research?...` lists them for the `user`). There is no separate record of the schedule: what ran is in `gb_agent_research_runs`.

How the scheduler works inside the server (the loop, the skipping, what is not remembered): [agent-scheduler.md](agent-scheduler.md).

Code: `internal/agent/agent_defs/cron.go` (the parser and `Next`), `agent_defs.Schedule`, `internal/agent_research/scheduler.go` (`Manager.RunSchedules`, started by the server after it is set up).

## Model

The model of the main agent is the request's `model`, else `model` of its definition, else the server's default. A worker of the investigative loop runs on the model of its supervisor.

## Versions

One file per name; `version` is metadata, and git keeps the history. Every run records `agent_name`, `agent_version` and `agent_hash` (the SHA-256 of the resolved content: prompt, skills and settings), so a run says exactly what it ran even when a file changed without a version bump. A request can require a version with `agent_version` (`1.2.0`, `>=1.0`, `<2`, `^1.2`, several separated by commas; `1.2` is any `1.2.x`): a mismatch is a `400`. Two versions of one name cannot be loaded side by side.

## Using a definition

- `POST /v1/gb/research` `{"topic": "...", "agent": "news_scanner", "agent_version": "^1.0", "max_cost_usd": 2}`; the MCP tool `research_start` has the same fields; `gligabao research start "..." --agent news_scanner --agent-version "^1.0" --max-cost 2`.
- `GET /v1/gb/research/agents` (`gligabao research agents`) lists the definitions: name, version, description, hash, source file, model, steps, caps, knowledge access, and skills (and `max_parallel`).
- `gligabao_agent --agent news_scanner [--agent-defs-dir DIR]` runs one without the server. By default it reads the same `agent_defs` directory next to its binary.

## In Modal

The container has no `agent_defs` directory, and baking the files into the image would need a new deployment for every prompt change. So the server sends the definitions with the job: a **bundle** (`agent_defs.Bundle`), JSON with the chosen definition, the prompts and the skills as text, at most 1 MiB. It travels `Remote.Start` → `POST /research/job_start` (`agent`, `agent_bundle`, `max_cost_usd`) → the sidecar writes it to the job's directory → `gligabao_agent --agent NAME --agent-bundle FILE --max-cost-usd N`. The binary checks the hash of every definition against its content, so a bundle that was changed on the way is refused.

## Checks

| Problem | Example of the error |
| ------- | -------------------- |
| unknown key | `unknown key(s): modle` |
| both or neither prompt | `system_prompt and system_prompt_file: give one of them, not both` |
| a path outside the directory | `system_prompt_file: "../x.md" is outside the definitions directory` |
| unknown tool, or a knowledge tool in `tools` | `tools: "query" is not listed here: the knowledge base tools come from [knowledge] access` |
| bad domain, both domain lists | `search: allowed_domains and blocked_domains cannot be used together for web_search` |
| a required skill that is missing | `required_skills: dates: "dates.md": file does not exist` |
| a knob over its ceiling | `investigate.max_parallel: 9 must be between 1 and 8 (or 0 for the default)` |
| the removed `[spawn]` table | `spawn: sub-agents of the simple loop were removed; ...` |
| a bad schedule | `schedule.cron: "* * * * *" runs the agent every 1m0s, at least 5m0s is needed between two runs`, `schedule.cron: minute: "61": 61 is out of range (0 to 59)`, `schedule.timezone: "Mars/Base" is not an IANA time zone like Europe/Berlin` |

## Not included yet

Reloading the definitions without a restart, several versions of one name side by side, other triggers than `on_demand` and `on_start` (after N steps, when a tool returns something), catching up the runs of a schedule that were missed while the server was down, a schedule that survives several servers (a lock in the database), a schedule that names its own runner, model or dataset, an admin UI view of the schedules, skills the agent loads on demand (today they are all in the prompt), a cap on the cost of a research in the admin UI (the API, MCP and CLI have it), a choice of the agent in the admin UI's start form, and a `research_agents` MCP tool. See the [roadmap](../roadmap/ROADMAP.md).
