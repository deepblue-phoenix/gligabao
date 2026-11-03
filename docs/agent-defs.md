# Agent definitions

A [research agent](research.md) is described by a **definition**: a TOML or JSON file that says what the agent is (its name, description, version and system prompt), how it works (model, steps, tools, search and knowledge-base settings), what it may spend (cost caps), which skills it has, and **when and which sub-agents it starts**. The files are written by hand, versioned with git next to the binary, and loaded once when the server starts. A research names the definition it wants (`"agent": "news_scanner"`); without a name it runs the built-in `default`, which is today's research agent.

This makes it easy to have several agents that search different areas, behave in a certain way and have preferences, with no change to the Go code.

Code: `internal/agent_defs` (the format, the loader, the checks, the registry, the bundle), `internal/agent/research.go` (`FromDef` builds the agent of a definition), `internal/agent_research` (the manager uses the definition of every agent it starts, the sub-agents included). Examples: `build/agent_defs/`.

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
tools = ["web_search", "fetch_url"]   # default: both; query and remember are NOT listed here: see [knowledge]

skills = ["citation_style", "dates"]  # agent_defs/skills/citation_style.md, ...; a missing file is a warning, the agent starts without it
required_skills = []                  # the same, but a missing file is a startup error

[search]    # how it searches the web
[knowledge] # how it uses the knowledge base
[limits]    # cost caps
[spawn]     # sub-agents
[schedule]  # runs by itself on a CRON schedule (optional)
```

Unknown keys are errors. Every field but `name`, `version`, `description` and the prompt is optional.

### The system prompt

`system_prompt` (or the text of `system_prompt_file`, a path relative to the file, inside the directory) is the **base** of the agent's prompt: it replaces the built-in research prompt. The runtime adds, in this order, what the tools need to be told: the search preferences, the part for the knowledge base (if it has the tools), the part for sub-agents (its roles, or the free-form policy), for a sub-agent its own preamble (its name and the reason its parent gave), and last the skills. So a prompt file is only about the agent's job and style.

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
- The settings belong to the agent: a sub-agent has those of its own definition, nothing is inherited.

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
- Dataset: a locked dataset of the definition, else the one of the request, else the default of the definition, else `main`. A **sub-agent** works in its parent's dataset (so siblings share what they remember) unless its own definition names one.
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
- The cost is what the run records (`cost_usd`): it comes from the price table, so a model with no price counts $0 and its caps cannot work. The `remember` and `query` calls are not in it ([deep-research-agent.md](deep-research-agent.md#what-a-run-costs)).
- In Modal the agent in the container enforces the caps itself (the definition and `--max-cost-usd` travel with the job). As a backstop the server ends a job that reports a cost 25% over the cap of the research; the run is then `stopped` ("stopped by the cost cap").

### `[spawn]`: sub-agents

```toml
[spawn]
enabled = true            # gives the agent the sub-agent tools
max_depth = 2             # these three can only lower the ceilings (2, 5 and 10); 0 is the ceiling
max_per_agent = 4
max_per_tree = 8          # of the root's definition

[[spawn.roles]]
name = "regional"                  # what the model picks in spawn_subagent (on_start: the name of the sub-agent)
agent = "regional_scout"           # the definition the sub-agent runs
version = ">=1.0, <2"              # optional: the version it must have (checked at startup)
trigger = "on_demand"              # on_demand (default) | on_start
when = "The topic names two or more regions that each need searches of their own."   # on_demand: told to the model
does = "Researches one region in depth and returns dated facts with URLs."          # told to the model; the reason of an on_start sub-agent
max_instances = 3                  # on_demand: most sub-agents of this role at once (0: only the limits above)
max_steps = 8                      # the steps it gets by default (default: max_steps of its definition)
max_steps_limit = 12               # the most the parent may ask for (default: max_steps of its definition)

[[spawn.roles]]
name = "headlines"
agent = "regional_scout"
trigger = "on_start"
does = "Reads today's headlines."
task_template = "Headlines about {{.Topic}}"   # Go template; {{.Topic}} is the topic of the research (default "{{.Topic}}")
```

- **Without roles**, `enabled = true` is the free-form behaviour of before definitions existed: the model spawns sub-agents that are **copies of itself** (same definition), with `spawn.md` as the policy. The built-in `default` works this way.
- **With roles**, the model sees only them. `spawn_subagent` gets a required `role` argument (an enum of the `on_demand` roles), and the prompt lists each role with its `does` and **`when`**: that is the policy of when to start it. A sub-agent runs the definition of its role: its prompt, tools, skills, search and knowledge settings, caps, and its own roles.
- **`on_start`**: the runtime starts the sub-agent **before the parent's first model call**, with the rendered `task_template` as its task, and tells the parent in its first turn that it was started, so it can work on its own part and call `wait_subagents`. The model does not decide. If it cannot be started, the parent is told.
- **Limits**: the code ceilings (depth 2, 5 per agent, 10 per tree, 40 steps) cannot be raised by a file. The depth counts from the root (the root is 0); an agent at its deepest level gets no spawn tool. `max_per_tree` is read from the root's definition. The steps of a sub-agent are the role's default; the parent may ask for more up to `max_steps_limit`.
- **Checks at startup**: a role must name an existing definition whose version meets `version`, the role's steps must fit, and no definition may start itself through its roles (a loop is an error).
- Every sub-agent is a run of its own, with its `agent_name`, `agent_version` and `agent_hash`.

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
- Only the server runs schedules: `gligabao_agent` ignores the section, and an agent that is only used as a sub-agent role is never started by it. The schedule is not part of `agent_hash` (when an agent runs is not what it is), so changing it does not make a new hash. It travels in a bundle but the container does nothing with it.
- `GET /v1/gb/research/agents` shows it: `schedule` with `cron`, `timezone`, `topic`, `user`, `paused` and `next_run`.
- The runs are ordinary researches (`agent_name` says which agent, `GET /v1/gb/research?...` lists them for the `user`). There is no separate record of the schedule: what ran is in `gb_agent_research_runs`.

Code: `internal/agent_defs/cron.go` (the parser and `Next`), `agent_defs.Schedule`, `internal/agent_research/scheduler.go` (`Manager.RunSchedules`, started by the server after it is set up).

## Model

The model of the main agent is the request's `model`, else `model` of its definition, else the server's default. A **sub-agent** runs on the `model` of its own definition, else on its parent's: the parent cannot choose it (`spawn_subagent` has no such argument).

## Versions

One file per name; `version` is metadata, and git keeps the history. Every run records `agent_name`, `agent_version` and `agent_hash` (the SHA-256 of the resolved content: prompt, skills and settings), so a run says exactly what it ran even when a file changed without a version bump. A request can require a version with `agent_version` (`1.2.0`, `>=1.0`, `<2`, `^1.2`, several separated by commas; `1.2` is any `1.2.x`): a mismatch is a `400`. Two versions of one name cannot be loaded side by side.

## Using a definition

- `POST /v1/gb/research` `{"topic": "...", "agent": "news_scanner", "agent_version": "^1.0", "max_cost_usd": 2}`; the MCP tool `research_start` has the same fields; `gligabao research start "..." --agent news_scanner --agent-version "^1.0" --max-cost 2`.
- `GET /v1/gb/research/agents` (`gligabao research agents`) lists the definitions: name, version, description, hash, source file, model, steps, caps, knowledge access, skills and roles.
- `gligabao_agent --agent news_scanner [--agent-defs-dir DIR]` runs one without the server. By default it reads the same `agent_defs` directory next to its binary.

## In Modal

The container has no `agent_defs` directory, and baking the files into the image would need a new deployment for every prompt change. So the server sends the definitions with the job: a **bundle** (`agent_defs.Bundle`), JSON with the chosen definition, those its roles start (and theirs), the prompts and the skills as text, at most 1 MiB. It travels `Remote.Start` → `POST /research/job_start` (`agent`, `agent_bundle`, `max_cost_usd`) → the sidecar writes it to the job's directory → `gligabao_agent --agent NAME --agent-bundle FILE --max-cost-usd N`. The binary checks the hash of every definition against its content, so a bundle that was changed on the way is refused.

## Checks

| Problem | Example of the error |
| ------- | -------------------- |
| unknown key | `unknown key(s): modle` |
| both or neither prompt | `system_prompt and system_prompt_file: give one of them, not both` |
| a path outside the directory | `system_prompt_file: "../x.md" is outside the definitions directory` |
| unknown tool, or a knowledge tool in `tools` | `tools: "query" is not listed here: the knowledge base tools come from [knowledge] access` |
| bad domain, both domain lists | `search: allowed_domains and blocked_domains cannot be used together for web_search` |
| a required skill that is missing | `required_skills: dates: "dates.md": file does not exist` |
| a limit over its ceiling | `spawn.max_per_agent: 6 must be between 0 (the ceiling) and 5, the ceiling` |
| a role that points nowhere or loops | `spawn.roles[0] (r).agent: no agent called "ghost"`, `a -> b -> a starts itself through its roles` |
| a role's version not met | `agent "x" is at version 1.0.0, which does not meet ">=2"` |
| a bad schedule | `schedule.cron: "* * * * *" runs the agent every 1m0s, at least 5m0s is needed between two runs`, `schedule.cron: minute: "61": 61 is out of range (0 to 59)`, `schedule.timezone: "Mars/Base" is not an IANA time zone like Europe/Berlin` |

## Not included yet

Reloading the definitions without a restart, several versions of one name side by side, other triggers than `on_demand` and `on_start` (after N steps, when a tool returns something), catching up the runs of a schedule that were missed while the server was down, a schedule that survives several servers (a lock in the database), a schedule that names its own runner, model or dataset, an admin UI view of the schedules, skills the agent loads on demand (today they are all in the prompt), a cap on the cost of a research in the admin UI (the API, MCP and CLI have it), a choice of the agent in the admin UI's start form, and a `research_agents` MCP tool. See the [roadmap](roadmap/ROADMAP.md).
