# Code execution for the investigative agent with Pydantic Monty

**Status:** implemented in 0.187.0 (see [As built](#as-built) for what differs from the plan). It adds one tool, `run_code`, to the [investigative loop](deep-research-loop.md) (`loop = "investigative"`, code in `internal/agent_research/investigate/` and `investigate_core/`), so that the agent can run a small piece of Python while it researches: compute from numbers it found, parse or filter a long text, cross-check datapoints, sort and join what it collected. The code runs in [Pydantic Monty](https://github.com/pydantic/monty), a sandboxed Python interpreter written in Rust, in a **sidecar process** of the gligabao process that runs the agent. The functions the code may call back into gligabao are Go functions, declared in one registry ([below](#host-functions)).

The simple loop (`loop = "simple"`) is not touched. The agent as it works today is in [deep-research-agent.md](../deep-research-agent.md), the definitions in [agent-defs.md](agent-defs.md), the run in [research.md](research.md).

## Why

The model is bad at arithmetic, at exact counting and at reading 200 rows of a table in its head, and every row it reads costs context. The loop already fights context bloat ([forced extraction, eviction](deep-research-loop.md#context-compaction)); code is the other half of the answer: the model writes a few lines, the sandbox does the exact work, and only the short printed result enters the prompt. It also makes [datapoint cross-checks](deep-research-loop.md#claims-and-cross-checking) (unit conversion, ratios, "do these two figures agree within 5%") deterministic instead of a judgement.

## Why Monty

- It is a **sandbox by construction**: no file system, no network, no environment, no imports of the host's modules unless the host hands them in; the only way out is the external functions the host registers. This matches the loop's rule that the code decides what may happen and the model decides content.
- It starts in microseconds and has **resource limits** (duration, heap memory, recursion depth), so a model-written infinite loop or a huge allocation ends the call, not the process.
- It runs in **worker subprocesses**, so even a crash of the interpreter (stack overflow, allocator abort) cannot take the host down.
- It supports **external functions**: when the code calls one, the interpreter pauses, the host runs it and resumes the code with the result. That is the mechanism for [host functions](#host-functions).

Monty is young (alpha, `0.0.x`) and its subset of Python is limited (no arbitrary third-party packages, a partial standard library). The prompt must say so, and the version is pinned ([Dockerfile](#dockerfile)).

## Constraints

- **Functional style** (mandatory, `AGENTS.md`): the host-function registry is an immutable value; a function's spec, its argument decoding, the generation of the stubs and of the prompt text are pure functions; the call into the sandbox, the process supervision and the host functions' effects are at the edge. The pure core (`investigate_core`) gets no new dependency on a process or on I/O (`purity_test.go` must still pass).
- **Minimal**: one tool, one sidecar, one registry. No second interpreter, no package installer, no persistent code workspace between runs.
- **Opt in**: off unless the agent definition turns it on. A server without Monty installed starts and works; an agent that needs code and has none fails to start with a clear message ([Failure modes](#failure-modes)).
- **Code cannot bypass the loop's rules.** It reads state; it does not change the frontier, the notes or the claims (the mutating tools stay model tools, so the gates, counters and breakers keep working). What it fetches goes through the same limits, domain rules, cost accounting and attempt log as a direct tool call.

## What the agent sees

One new tool, offered in the turns where the other research tools are offered (the normal exploration turns and the planning turn), **not** in the extraction, consolidation and synthesis turns (synthesis has no search tool, and the report must come from the notes). It is added through the `External` list of the loop's `Config`, the way `web_search` and `fetch_page` are, so `OfferedTools` and `dispatch` need no new concept.

```json
{
  "name": "run_code",
  "description": "Run a short Python program in a sandbox (Monty, a subset of Python: no imports of third-party packages, no files, no network). Use it for exact arithmetic, unit conversion, counting, sorting, parsing or filtering text you already have, and comparing numbers. Print what you want to read; only the printed output and the value of the last expression come back. Functions you may call are listed below. A run is stateless: variables do not survive to the next call.",
  "input_schema": {
    "type": "object",
    "properties": {
      "code": {"type": "string", "description": "the Python program"},
      "inputs": {"type": "object", "description": "named JSON values bound as variables before the program runs (e.g. a table you parsed)"}
    },
    "required": ["code"]
  }
}
```

The tool description ends with the **function list generated from the registry** (name, Python signature, one-line doc), so the text the model reads can never drift from what is callable.

Result the model reads (compact text, capped by `max_output_chars`, never the raw protocol):

```
ok
value: 0.8421
stdout:
mean=0.8421 n=12
```

or, on a failure, `error: <kind>: <message>` with the traceback line numbers (kinds: `syntax`, `type`, `runtime`, `timeout`, `memory`, `crashed`, `host_function`, `unavailable`). An error is a normal tool error: the model reads it and goes on (it may fix the code).

## Architecture

```
gligabao (server)  or  gligabao_agent
 └─ investigate loop ── run_code ──► internal/monty (Go client, supervisor)
                                        │  JSON lines over the child's stdin/stdout
                                        ▼
                                   monty bridge (Python, pydantic-monty client)   ← the sidecar
                                        │  pool of workers, one session per run_code call
                                        ▼
                                   `monty subprocess` workers (Rust, sandboxed)
                                        ▲
        host function calls ◄───────────┘  (bridge asks Go; Go answers; the code resumes)
```

### Why a sidecar, and which process owns it

Monty has Python and Rust APIs and no Go one. The sidecar is a **child process of whichever gligabao process runs the agent**: `gligabao serve` (the agent runs in the server) or `gligabao_agent` (the standalone binary; the CLI daemon is a `serve`). Each process that runs an investigative agent with code enabled owns its own sidecar:

- **Started lazily**, at the first `run_code` of the process (not at startup), so a server whose agents never use code pays nothing. One sidecar is shared by all the runs and sub-processes of that process, with a bounded worker pool (`monty_workers`).
- **Supervised**: if the sidecar dies, the next call restarts it (at most a few restarts in a row, then `unavailable` until the process restarts, so a broken install does not spin). In-flight calls of the dead sidecar fail with `crashed`.
- **Stopped with its owner**: closing its stdin ends it; on `serve` shutdown and when `gligabao_agent` exits the supervisor also terminates and then kills the process group, so no orphan stays.
- **Not the training sidecar.** The existing [sidecar](../training/sidecar.md) is a separate HTTP service for Modal. This one is a local child process, with no port and no network surface: the pipe is private to the parent.

The sidecar is the thin bridge below, not Monty itself, because the stable public interface of Monty is its Python client (`pydantic_monty`: `Monty` pool, `checkout()` sessions, `feed_start` snapshots for external calls, `ResourceLimits`); the `monty subprocess` wire format (framed protobuf, the `monty-proto` crate) is an internal protocol of a `0.0.x` project. Speaking it directly from Go (one less runtime in the image) is a possible later change that stays behind the same `internal/monty` interface ([Leftovers](#leftovers)).

### The bridge protocol

One JSON object per line, UTF-8, both directions. Go is the client; every message carries the `id` of the `run_code` call it belongs to, so calls run concurrently on one sidecar.

Go to bridge:

| `type` | Fields | Meaning |
| ------ | ------ | ------- |
| `hello` | `protocol`, `functions` (name, Python signature stub), `limits_default` | first message: the registry's stubs, so the bridge can answer name lookups and type-check |
| `run` | `id`, `code`, `inputs`, `limits` (`max_duration_s`, `max_memory_mb`, `max_recursion`), `type_check` | run a program in a fresh session |
| `result` | `id`, `call_id`, `ok`, `value` or `error` | the answer to a host function call |
| `cancel` | `id` | stop a run (the run's context ended) |
| `shutdown` | | end |

Bridge to Go:

| `type` | Fields | Meaning |
| ------ | ------ | ------- |
| `ready` | `protocol`, `monty_version` | the bridge started and found the binary |
| `call` | `id`, `call_id`, `name`, `args`, `kwargs` | the code called a host function |
| `done` | `id`, `value`, `stdout`, `stderr`, `stats` (wall ms, host calls) | the program finished |
| `failed` | `id`, `kind`, `message`, `line` | one of the error kinds above |

`protocol` is an integer; a mismatch on `ready` is `unavailable` with the two versions in the message. The bridge file is embedded in the Go binary (`go:embed`) and started as `python3 -c <script>` (or the interpreter in `GLIGABAO_MONTY_PYTHON`), so there is no loose file to deploy. Values cross as JSON (Monty's conversion covers `None`, bool, int, float, str, list, dict, tuple); anything else is a `host_function` error naming the type.

## Host functions

"The Go functions exposed to the Monty environment" are one declared, typed, immutable registry. Nothing is callable from the sandbox that is not in it, and the registry is the single source for four things: the callable names, the argument decoding, the Python stubs sent to the bridge (for name lookup and the optional type check), and the function list in the tool description.

Package `internal/monty/hostfn` (no dependency on the process code):

```go
// Spec declares one function of the sandbox's world. Immutable.
type Spec struct {
    Name   string   // Python name: lowercase snake_case, prefix by area (notes_, frontier_, claims_, kb_, web_)
    Doc    string   // one line for the model
    Params []Param  // typed, in call order: name, type (str|int|float|bool|list[str]|...), required, default
    Result string   // the Python return type, for the stub and the doc
    Class  Class    // ClassRead (state only), ClassFetch (reaches the outside world, costs, is capped)
    Handler Handler // func(ctx context.Context, env Env, args Args) (any, error)
}

// Registry is the immutable set of specs by name. Constructors only: New(specs...) validates (unique names, valid identifiers,
// params consistent) and returns an error; there is no Register on a shared global.
type Registry struct{ specs []Spec }

func (r Registry) Stubs() string              // the .pyi-style text sent in `hello`
func (r Registry) Describe() string           // the list appended to the tool description
func (r Registry) Call(ctx, env, name string, args []any, kwargs map[string]any) (any, error)
```

`Env` is the read-only view a handler gets for one `run_code` call: a snapshot of the loop `State` (frontier, notes, claims, challenges, as of the start of the call), the budgets, and the narrow interfaces to the effects it may use (the fetch and search tools, the knowledge query). It never exposes the DB, the session or the model client. A handler is a function of `(Env, Args)`; the effects it performs are the ones of its `Class`, executed by the loop's existing paths.

### The first set

Grouped by area; all arguments and results are plain JSON-like values.

| Function | Class | What it does |
| -------- | ----- | ------------ |
| `notes_read() -> str` | read | the current `notes.md` text |
| `frontier_get(id: str) -> dict` | read | one question with its status, answer, criteria and attempt log |
| `frontier_list(status: str = "") -> list[dict]` | read | questions, optionally of one status |
| `claims_list(status: str = "") -> list[dict]` | read | the claim ledger: id, text, status, sources, datapoint value and unit when it is one |
| `fetch_page(url: str, max_chars: int = 0) -> str` | fetch | the same tool the model has: domain rules, `max_page_chars`, `max_calls` and cost accounting apply; the call is logged as an attempt of the current question |
| `web_search(query: str) -> str` | fetch | likewise |
| `kb_query(question: str) -> str` | fetch | `query` of the knowledge base (only when `[knowledge]` allows read) |
| `log(message: str) -> None` | read | adds a line to the call's trace (not to the prompt) |

What is **not** exposed in this version, on purpose: any function that changes the frontier, the notes or the claims (`add_question`, `resolve_question`, `notes_append`, `record_claim`, ...), `remember`, the file system, the clock, randomness, the environment, the sub-agent spawn. The model records what it learned from the code through the normal tools, so the loop's gates and counters see it. Adding a function is one `Spec` plus its handler and a test; it needs no change to the bridge or to the loop.

### Rules every call follows

- **Budgets.** `max_host_calls` per `run_code` call and per run (`[code]`, below); a `fetch` class call also counts against the run's `fetch_url` / `web_search` `max_calls` and its cost against the cost cap, exactly as if the model had called it. Over a budget the function raises a Python exception inside the sandbox (`BudgetExceeded: ...`), which the code can catch or which ends the program.
- **Errors are values.** A handler error becomes a Python exception with the message in the sandbox (`HostError`), never a crash of the bridge.
- **Cancellation.** A host call runs under the context of the run; a stopped run cancels the call and the program.
- **Size.** A result over `max_result_chars` is refused with an error that says to ask for less (`fetch_page(url, max_chars=...)`), so the sandbox cannot be used to pull a megabyte into a variable and then the context.
- **Order.** Calls from one program are serial (no async externals in v1).

## Agent definition

A new optional `[code]` table in the [agent definition](agent-defs.md), accepted only with `loop = "investigative"` (an **error** with the simple loop, like `[investigate]`: not a silent no-op):

```toml
loop = "investigative"
[code]
enabled = false            # the switch; the table without enabled = true is an error
timeout_s = 10             # wall time of one run_code call, 1 to 60
max_memory_mb = 128        # Monty heap limit, 8 to 1024
max_recursion = 500
max_output_chars = 4000    # what the model reads back of stdout + value, 200 to 20000
max_calls = 30             # run_code calls per run, 0 = no limit
max_host_calls = 20        # host function calls per run_code call
max_result_chars = 20000   # one host function result
functions = []             # names allowed; empty = all of the registry
type_check = false         # run Monty's type checker (against the stubs) before running
```

`functions` narrows the registry per agent (a research that must not fetch from code lists only the `read` ones); a name that is not in the registry is a startup error, with every other problem of the file in the same message. A research request can lower the numbers through the `investigate`-style override only if a need shows up; v1 has no request option.

Server-wide settings (`gligabao.toml`, the matching `serve` / `gligabao_agent` flags and environment variables):

| Setting | Default | Meaning |
| ------- | ------- | ------- |
| `monty_bin` / `MONTY_BIN` | looked up next to the interpreter, then `PATH` | the `monty` worker binary |
| `monty_python` / `GLIGABAO_MONTY_PYTHON` | `python3` | the interpreter that runs the bridge (has `pydantic_monty` installed) |
| `monty_workers` | 2 | the most worker processes of the sidecar |
| `monty_request_timeout_s` | 90 | parent-side deadline that kills a stuck worker (a backstop above `timeout_s`) |

A definition with `[code] enabled = true` on a server where the sidecar cannot be found is a **startup error** for that definition at load time (a cheap check: the interpreter and `pydantic_monty` import, the binary exist), so it is found at boot, not in the middle of a run.

## Changes in the investigative loop

All at the edge; the pure core is unchanged except for the data that describes the tool.

- `investigate_core/prompt.go`: a short block in the loop's instructions, only when the tool is offered: what `run_code` is for, that it is stateless, that it cannot change the frontier, that exact numbers should be computed rather than estimated, and that a computed datapoint is recorded as a claim with its sources (the code is not a source).
- `investigate/loop.go`: `run_code` is an `agent.Tool` in `cfg.External`, built by `FromDef` when `[code]` is enabled, so it reuses `runExternal` (usage and cost draining, attempt logging for raw tools). `run_code` is not a raw tool (it is not an attempt at a question by itself); its inner `fetch_page` and `web_search` calls are logged as attempts through the host function path, with the tool name of the real tool.
- Context: a `run_code` output is capped by `max_output_chars`, so the forced-extraction threshold rarely applies; if it does, the existing rule is used as is.
- Trajectory (`atif`): the step of a `run_code` call records the code, the printed output, the stats and, as nested calls, the host functions it made, so a run can be audited afterwards. The admin UI's investigation view does not change in this version.
- Breakers and cost: the sandbox costs no tokens; its host calls are counted as above. The wall time of `run_code` counts against the run's own time limit if the definition has one. A `run_code` that fails does not count as a wasted attempt of a question.

## Dockerfile

The runtime stage of `Dockerfile` installs Monty: the Python bridge client and the worker binary, pinned. The final image stays Debian slim; the Go binary is still static.

```dockerfile
FROM debian:bookworm-slim
ARG MONTY_VERSION=0.0.21
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates python3 python3-venv \
    && python3 -m venv /opt/monty \
    && /opt/monty/bin/pip install --no-cache-dir "pydantic-monty==${MONTY_VERSION}" \
    && apt-get purge -y --auto-remove python3-venv \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --no-create-home gligabao && mkdir /data && chown gligabao /data
ENV GLIGABAO_MONTY_PYTHON=/opt/monty/bin/python MONTY_BIN=/opt/monty/bin/monty
```

- `pydantic-monty` is the metapackage: it installs `pydantic-monty-client` (the `pydantic_monty` module) and `pydantic-monty-runtime` (the `monty` binary, put in `bin/` of the venv). Python 3.11 of Debian bookworm satisfies the `>=3.10` requirement.
- The venv is world-readable and the `gligabao` user (10001) only runs it: no write access is needed at run time (`PYTHONDONTWRITEBYTECODE=1` can be set to keep it so).
- The version is a build `ARG`, so an upgrade is a one-line change; the bridge's `protocol` check and the integration test ([Tests](#tests)) catch an incompatible release.
- The comment above the `ENV` line in the Dockerfile says what the venv is for, in the style of the other lines. The `docker run` documentation in the docs (the README / deploy notes that mention the image) says that the image includes the code sandbox and that it is only used by agents with `[code] enabled`.
- `Dockerfile.sidecar` (training/Modal) is unchanged: the code sandbox does not run in Modal, since an investigative agent is local only.

A bare-metal install (`go install`, the release binaries) needs `pip install pydantic-monty` somewhere and `monty_python` / `monty_bin` pointing at it; the docs say so, and `gligabao doctor`-style output (the startup log) prints whether the sandbox is available.

## Failure modes

| Situation | Behaviour |
| --------- | --------- |
| `[code] enabled` and no sandbox installed | the definition fails to load with the missing piece named (interpreter, `pydantic_monty` import, `monty` binary) |
| Sidecar crashes during a call | that call returns `error: crashed`; the next call restarts it (bounded) |
| Program loops forever / allocates too much | Monty's limits end it: `timeout` / `memory`; a worker that ignores them is killed by the parent-side deadline |
| Host function raises, over budget | a Python exception inside the sandbox; uncaught, the call ends with `host_function` and the message |
| Run stopped (`Manager.Stop`, a breaker) | the context ends, the bridge gets `cancel`, the program and its worker are stopped |
| Output too large | truncated at `max_output_chars` with a `[truncated N chars]` marker |
| Protocol version mismatch | `unavailable`, both versions in the message |

## Security notes

- The sandbox has no I/O of its own. Its only reach is the registry. Network and file access exist only as `fetch` class functions that go through the existing domain rules (`allowed_domains` / `blocked_domains`, redirects checked). The injected content of a fetched page can steer the model into writing code, so nothing in the registry may write outside the run (this is why the first set has no mutating or `remember` functions).
- The sidecar runs as the same OS user as gligabao, with no extra privileges and no listening port. Memory and CPU are bounded by Monty's limits and the worker cap; in Docker the container limits are the outer wall.
- The code and its output are stored in the trajectory of the run; they are subject to the same retention as the rest of it.

## Tests

- **Pure** (no process): the registry (`New` rejects duplicate or invalid names; `Stubs` and `Describe` golden tests; argument decoding errors; the `functions` allow-list narrowing), the `[code]` loader checks (rejected with the simple loop, out-of-range numbers, unknown function), the output cap and the result formatting.
- **Protocol** (a fake bridge, a small Go test binary that speaks the JSON lines): ordering, concurrent runs, `call`/`result` round trips, cancel, a bridge that dies mid-call, a protocol mismatch, restart limits.
- **Loop**: with a fake `run_code` in `External`, the tool is offered in exploration and planning turns and not in extraction, consolidation and synthesis; a `fetch_page` made from code counts against `max_calls` and the cost cap and is logged as an attempt; `purity_test.go` still passes.
- **Integration** (skipped when `monty_python` / `MONTY_BIN` are not available, like the other tests that need an external tool): real Monty: arithmetic, an infinite loop ends with `timeout`, a memory bomb ends with `memory`, a host function round trip, a `crashed` worker is replaced.
- **Docker**: a build check that `/opt/monty/bin/monty -c "print(1+1)"` runs as the `gligabao` user.

## Implementation order

1. `internal/monty/hostfn`: `Spec`, `Registry`, the read-only functions, the stub and description generators, all with their pure tests.
2. The bridge script and `internal/monty`: the client, the protocol types, the supervisor (lazy start, restart bound, shutdown), tested against the fake bridge, then against real Monty.
3. The `[code]` table in `internal/agent/agent_defs` (format, loader checks, the startup availability check) and the server settings and flags in `serve` and `gligabao_agent`.
4. `run_code` as an `agent.Tool` and its wiring in `FromDef` and `investigate.Config.External`; the prompt block; the trajectory recording.
5. The `fetch` class functions (`fetch_page`, `web_search`, `kb_query`) with the budgets, cost accounting and attempt logging.
6. The `Dockerfile` change and its build check.
7. An example agent in `build/agent_defs/` (an investigative agent with `[code] enabled = true`), an eval or a few scripted runs comparing a numeric-heavy topic with and without code.
8. Docs and release: update [agent-defs.md](agent-defs.md) (the `[code]` table), [research.md](research.md#the-investigative-loop), [deep-research-agent.md](../deep-research-agent.md) and the deployment notes; the `docs/roadmap/ROADMAP.md` row; `VERSION` and `CHANGELOG.md` (a minor bump: new tool, new config and a new dependency of the image, with a note that nothing changes for an agent that does not set `[code] enabled`).

## As built

Where the code is: `internal/monty/hostfn` (registry and the first set), `internal/monty` (`bridge.py`, embedded; `Sidecar`, the supervisor and client; `Runner` interface), `internal/agent_research/investigate/code.go` (the `run_code` tool, built by `investigate.Run` from `Config.Code`, not in `FromDef`: it needs the run's state and its fetch tools), `agent_defs.Code` (the `[code]` table), `investigate_core.ToolRunCode` and the prompt block, `Manager.Monty` / `Manager.Sandbox`.

Differences from the plan above:

- **Bridge**: the bridge uses `feed_run` with `external_lookup` (one thread per run, one worker session per run) instead of `feed_start` snapshots; the host call blocks the thread until Go answers. The list of allowed functions and the stubs travel in each `run` message (`functions`, `stubs`), so agents with different `functions` share one sidecar; `hello` carries the full registry. `monty_workers` and `monty_request_timeout_s` reach the bridge as the environment variables `GLIGABAO_MONTY_WORKERS` and `GLIGABAO_MONTY_REQUEST_TIMEOUT`.
- **Settings**: `monty_python`, `monty_bin`, `monty_workers` and `monty_request_timeout_s` are in `gligabao.toml` (and `MONTY_BIN` / `GLIGABAO_MONTY_PYTHON` in the environment, which `gligabao_agent` uses); there are no CLI flags.
- **Availability check**: it is made when a run of an agent with `[code] enabled` starts (the run fails with the missing piece named), not when the definitions load.
- **Budgets**: `max_host_calls` is per `run_code` call only; the `fetch` class functions go through the agent's own tools, so their limits, cost and attempt log apply as for a direct call. `fetch_page` and `web_search` appear in the function list only when the agent has the tool, and `kb_query` only when it has the knowledge `query` tool.
- **Trajectory**: the step of a model call that used `run_code` has `extra.code_runs`: for each call the code, the output, `wall_ms`, the host function calls (function, args, kwargs) and the `log()` lines.
- **Example**: `build/agent_defs/numeric_investigator.toml`.

## Leftovers

Left out of this plan on purpose; record as [leftover tasks](../../leftover_tasks/) when the work lands:

- Speaking the `monty subprocess` protocol (or `monty-server` over WebSocket) directly from Go, to drop Python from the image.
- State that survives between `run_code` calls of a run (a REPL session per run), and `mount`ed read-only data (the run's fetched pages as files).
- Code in the synthesis phase, in sub-agents, in the simple loop and in `modal` runs.
- Mutating host functions (recording a computed datapoint as a claim straight from the code) with the gates applied.
- Showing the code calls in the admin UI's investigation view.
