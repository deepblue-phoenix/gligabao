# Sub-agents

A [research agent](research.md) can split its work: it can **spawn sub-agents**, other research agents that each take a sub-task, each with a **reason** to exist. Sub-agents work alone: they get a task, do it and hand a report to their parent (they cannot message each other). This page is about that capability. The agent itself (its loop, its web and knowledge-base tools) is in [deep-research-agent.md](../deep-research-agent.md); the job around it (runs, states, REST, CLI) is in [research.md](research.md).

Code: `internal/agent/team.go` (the tools), `internal/agent/prompts/` (the prompts, one Markdown file each: see [Prompts](#prompts)), `internal/agent_research/team.go` (spawning and the limits) and `internal/agent_research/researcher.go` (the place of each agent in the tree).

It is on for every research agent that runs as a job on the server. There is no switch to turn it off: the [limits](#limits) bound what it can cost.

## Concepts

- **Parent / sub-agent.** The agent that calls `spawn_subagent` is the **parent**, the agent it starts is the **sub-agent**. A research a user starts (`POST /v1/gb/research`) is a **root**: it has no parent.
- **Siblings** are the sub-agents of the same parent. They do not talk to each other: the only way information moves is the task down and the report up (and what they `remember` into the shared dataset).
- **Name.** The parent gives each sub-agent a name when it spawns it. The name is unique among the sub-agents of one parent, so the parent uses it to wait for a sub-agent. A root has no name.
- **Reason.** With every `spawn_subagent` the parent submits a reason: why this sub-agent has to exist. It is stored with the run, the sub-agent is told it in its prompt. A root has no reason.

```
root (research started by the user)
├── alpha            ┐ siblings: the sub-agents of one parent
├── beta             ┘
│   └── beta-1         child of beta
└── gamma
```

## Tracking

A sub-agent is tracked like every other agent. It is a run in `gb_agent_research_runs` with its own states (`gb_agent_research_states`), status, usage (steps, tokens, `cost_usd`), `report` and `error`, owned by the same user as its parent, so that it shows in the list and get endpoints, the CLI and MCP tools like any run. What is new on a run:

| Field / column | Meaning |
| -------------- | ------- |
| `parent_id` | the run of the agent that spawned it; `null` for a research a user started |
| `name` | the name the parent gave it; `""` for a root |
| `reason` | why the parent spawned it (submitted with `spawn_subagent`); `""` for a root |

- `(parent_id, name)` is unique: a parent cannot have two sub-agents with one name. Deleting a run deletes its sub-agents (`ON DELETE CASCADE`).
- A sub-agent runs on the **same model and dataset** as its parent, so with a knowledge base siblings share what they `remember`. Its `topic` is the task it was given.
- `GET /v1/gb/research?parent_id=<run id>` lists the sub-agents of a run (`parent_id` is also in `ListOptions` of the `Manager`). Without it, the list has all runs, sub-agents included; tell them apart by `parent_id`.
- A database made before this feature gets the three columns (`parent_id`, `name`, `reason`) and their indexes when the server starts (`agent_research.Open`; idempotent).
- Sub-agents do not count in the number of researches a user may have working at once (3); the [limits](#limits) below bound them instead.
- The states have two more values for the tools of this page: `spawning` and `waiting` (see [research.md](research.md#runs-and-states)).

Each sub-agent works like any agent: a `researcher` struct of its own, a goroutine of its own, its own HTTP client. They share the database and LLM clients and nothing else (see [isolation](../deep-research-agent.md#isolation)); the only thing that connects them is the parent's list of its sub-agents (a `family`, guarded by a lock).

## Spawning

### Tool `spawn_subagent`

Input: `{"name": "...", "reason": "...", "task": "...", "max_steps": 10}`.

- `name` (required): 1 to 40 letters, digits, `-` or `_`; unique among the agent's sub-agents.
- `reason` (required, at most 300 characters): why this sub-agent has to exist, in a sentence: what it covers that the agent cannot cover cheaply itself. A missing reason is an error that sends the agent back to decide whether it needs the sub-agent at all.
- `task` (required, at most 2000 characters): what the sub-agent has to do. It knows nothing of its parent's conversation, so the task must be self-contained and say what to bring back.
- `max_steps` (optional): the most model calls the sub-agent makes; default 10, at most 40.

The call records the run, starts the sub-agent in the background and **returns at once** (`started sub-agent "alpha" (run 12): ...`), so the parent can spawn several and go on with its own work. An error (a bad name, a name already used, a limit reached) is returned to the model as the result of the tool, like any tool failure, and does not fail the run.

What the prompts say is in [Prompts](#prompts).

### Tool `wait_subagents`

Input: `{"names": ["alpha", "beta"]}` (`names` optional: all the agent's sub-agents). It **blocks** until those sub-agents have ended and returns, for each, `## <name> (run <id>): <status>` and its report; for a sub-agent that failed (or was cancelled), the error instead. A name that is not a sub-agent of the caller is an error that lists the names it has; calling it with no sub-agents is an error too. The agent's state is `waiting` meanwhile. A sub-agent that failed does not fail its parent: the parent is told and decides.

The parent need not call `wait_subagents`: it may end first (see [When an agent ends](#when-an-agent-ends)).

### Roles of an agent definition

With [agent definitions](agent-defs.md) the model does not spawn copies of itself: when its definition has `[[spawn.roles]]`, `spawn_subagent` takes a required `role`, and the sub-agent runs the definition of that role (its own prompt, tools, search and knowledge settings, caps and roles). A role says **when** the sub-agent is started (`on_demand`: the model decides, guided by the role's `when`; `on_start`: the runtime starts it before the parent's first model call, with a task from `task_template`) and **what it does** (the definition, and `does`). The sub-agent runs on the model of its own definition, else its parent's, and in its parent's dataset unless its definition names one. A definition without roles keeps the free-form behaviour described on this page. The run of a sub-agent records its `agent_name`, `agent_version` and `agent_hash`.

### Limits

They are constants in `internal/agent/agent_defs` (aliased in `internal/agent_research/team.go`); a definition may only lower them (`spawn.max_depth`, `max_per_agent`, `max_per_tree`):

| Limit | Value |
| ----- | ----- |
| sub-agents of one agent (`MaxSubagentsPerAgent`) | 5 |
| sub-agents in the whole tree of a research (`MaxSubagentsPerTree`) | 10 |
| generations of sub-agents (`MaxSubagentDepth`) | 2: a root spawns sub-agents, which may spawn their own; those get no `spawn_subagent` tool |
| `max_steps` of a sub-agent | default 10 (`DefaultSubagentMaxSteps`), at most 40 |

The worst case of a research is therefore its own `max_steps` plus 10 sub-agents of up to 40 steps each, each with the web searches it makes. A sub-agent's cost is its own (`cost_usd` of its run): it is **not** added to the parent's, but a [cost cap](agent-defs.md#limits-cost-caps) of the research counts all the agents together: when it is reached every agent of the tree stops at its next model call, no more sub-agents can be started, and the reports say so.

### When an agent ends

When an agent ends, whether it wrote its report, failed or was interrupted, the sub-agents it spawned that are still working are **cancelled**: they end `failed` with `error` "cancelled: the agent that spawned it ended", and the agent's own run is finished **after** them, so the records are in order (a parent never finishes before its sub-agents). When the server stops, every agent is interrupted as before ("interrupted: the server stopped while the agent worked"), and a run left `running` by a dead process is marked `failed` when the server starts again.

## Prompts

The prompts are not in the Go code: each is a Markdown file in `internal/agent/prompts/`, embedded in the binary (`go:embed`), and the package exposes them as strings (`prompts.Research`, `prompts.Spawn`, ...). The system prompt of an agent is made of parts joined by a blank line (`prompts.Join`):

| File | Part | Given to |
| ---- | ---- | -------- |
| `research.md` | the base: research the web, check claims, write the report | every research agent |
| `knowledge.md` | how to use `query` and `remember` | agents with the knowledge base |
| `spawn.md` | **when and how to spawn sub-agents** | agents with `spawn_subagent` |
| `subagent.md` | who it is (name), why it was spawned (reason), that its answer goes to its parent | sub-agents |
| `web_search.md` | the system prompt of the call the `web_search` tool makes | not part of the agent's prompt |

`spawn.md` is the policy of the agent that starts a research. It says that the default is **not** to spawn (every sub-agent costs as much as a full agent) and that the agent decides, before it starts searching, whether spawning is really warranted: only if the topic splits into two or more independent parts, each needing research of its own (clearly more than a few searches), too much to do one after the other within its own steps; never for a narrow or factual question, a topic a few searches cover, a part that needs the result of another, or to double-check itself. If it does spawn it gives each sub-agent a reason (and does not spawn one it cannot write a reason for), uses as few as it needs (usually 2 to 4), gives self-contained, narrow, non-overlapping tasks that say what to bring back, starts them in one step, researches its own part meanwhile, then waits, compares the reports and writes its own.

`subagent.md` is a template (`text/template`: the name and the reason); a sub-agent that can spawn is told to do so only if its own task is clearly too big.

## Example

A user starts a research on "the electric-vehicle battery market". The root (run 1) thinks, then spawns two sub-agents in one step:

```
spawn_subagent {"name": "chemistry", "reason": "chemistries and costs are a research of their own, separate from the industry side", "task": "What battery chemistries are in EVs today and next, with energy density and cost per kWh? Report figures and source URLs."}
spawn_subagent {"name": "market",    "reason": "market shares need their own search of recent industry reports", "task": "Who are the biggest EV battery makers and their market shares in the latest year? Report figures and source URLs."}
```

Both return at once, runs 2 and 3 start (`parent_id: 1`, `name: "chemistry"` / `"market"`), and `GET /v1/gb/research?parent_id=1` lists them while they work. The root calls `wait_subagents {}` (state `waiting`). Meanwhile each works alone on its task. When both end, `wait_subagents` returns their two reports, and the root writes its own, which is the report of run 1.

## Not included yet

Messages between agents (removed in 0.169.0: sub-agents work alone), cancelling one sub-agent from its parent (an admin can stop any agent from the [admin UI](research.md#admin-ui)), a per-user or server-wide setting that turns sub-agents off, and counting the cost of the sub-agents in their parent's `cost_usd`. See the [roadmap](../roadmap/ROADMAP.md).
