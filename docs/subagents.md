# Sub-agents and messages

A [research agent](research.md) can split its work: it can **spawn sub-agents**, other research agents that each take a sub-task, each with a **reason** to exist, and **sibling** sub-agents can **list each other** and **send each other messages**. This page is about that capability. The agent itself (its loop, its web and knowledge-base tools) is in [deep-research-agent.md](deep-research-agent.md); the job around it (runs, states, REST, CLI) is in [research.md](research.md).

Code: `internal/agent/team.go` (the tools), `internal/agent/prompts/` (the prompts, one Markdown file each: see [Prompts](#prompts)), `internal/agent_research/team.go` (spawning, the limits, who the siblings are), `internal/agent_research/messages.go` (the message system) and `internal/agent_research/researcher.go` (the place of each agent in the tree).

It is on for every research agent that runs as a job on the server. There is no switch to turn it off: the [limits](#limits) bound what it can cost.

## Concepts

- **Parent / sub-agent.** The agent that calls `spawn_subagent` is the **parent**, the agent it starts is the **sub-agent**. A research a user starts (`POST /v1/gb/research`) is a **root**: it has no parent.
- **Siblings** are the sub-agents of the same parent. A sub-agent's children are **not** its siblings' siblings: they are one generation down, and cannot be reached by messages from the generation above (see [Messages](#messages)).
- **Name.** The parent gives each sub-agent a name when it spawns it. The name is unique among the sub-agents of one parent, so the parent uses it to wait for a sub-agent and siblings use it to address each other. A root has no name.
- **Reason.** With every `spawn_subagent` the parent submits a reason: why this sub-agent has to exist. It is stored with the run, the sub-agent is told it in its prompt, and its siblings can read it (`list_siblings`), so that they can decide whether it is the one to ask for help. A root has no reason.

```
root (research started by the user)
├── alpha            ┐ siblings: can message each other
├── beta             ┘
│   └── beta-1         child of beta: sibling of the other children of beta, not of alpha
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
- The states have three more values for the tools of this page: `spawning`, `waiting` and `messaging` (see [research.md](research.md#runs-and-states)).

Each sub-agent works like any agent: a `researcher` struct of its own, a goroutine of its own, its own HTTP client. They share the database and LLM clients and nothing else (see [isolation](deep-research-agent.md#isolation)); the only things that connect them are the parent's list of its sub-agents (a `family`, guarded by a lock) and the message system.

## Spawning

### Tool `spawn_subagent`

Input: `{"name": "...", "reason": "...", "task": "...", "max_steps": 10}`.

- `name` (required): 1 to 40 letters, digits, `-` or `_`; unique among the agent's sub-agents.
- `reason` (required, at most 300 characters): why this sub-agent has to exist, in a sentence: what it covers that the agent cannot cover cheaply itself. A missing reason is an error that sends the agent back to decide whether it needs the sub-agent at all. It is the text siblings see in `list_siblings`.
- `task` (required, at most 2000 characters): what the sub-agent has to do. It knows nothing of its parent's conversation, so the task must be self-contained and say what to bring back.
- `max_steps` (optional): the most model calls the sub-agent makes; default 10, at most 40.

The call records the run, starts the sub-agent in the background and **returns at once** (`started sub-agent "alpha" (run 12): ...`), so the parent can spawn several and go on with its own work. An error (a bad name, a name already used, a limit reached) is returned to the model as the result of the tool, like any tool failure, and does not fail the run.

What the prompts say is in [Prompts](#prompts).

### Tool `wait_subagents`

Input: `{"names": ["alpha", "beta"]}` (`names` optional: all the agent's sub-agents). It **blocks** until those sub-agents have ended and returns, for each, `## <name> (run <id>): <status>` and its report; for a sub-agent that failed (or was cancelled), the error instead. A name that is not a sub-agent of the caller is an error that lists the names it has; calling it with no sub-agents is an error too. The agent's state is `waiting` meanwhile. A sub-agent that failed does not fail its parent: the parent is told and decides.

The parent need not call `wait_subagents`: it may end first (see [When an agent ends](#when-an-agent-ends)).

### Limits

They are constants in `internal/agent_research/team.go`:

| Limit | Value |
| ----- | ----- |
| sub-agents of one agent (`MaxSubagentsPerAgent`) | 5 |
| sub-agents in the whole tree of a research (`MaxSubagentsPerTree`) | 10 |
| generations of sub-agents (`MaxSubagentDepth`) | 2: a root spawns sub-agents, which may spawn their own; those get no `spawn_subagent` tool |
| `max_steps` of a sub-agent | default 10 (`DefaultSubagentMaxSteps`), at most 40 |

The worst case of a research is therefore its own `max_steps` plus 10 sub-agents of up to 40 steps each, each with the web searches it makes. There is no cap in dollars yet. A sub-agent's cost is its own (`cost_usd` of its run): it is **not** added to the parent's.

### When an agent ends

When an agent ends, whether it wrote its report, failed or was interrupted, the sub-agents it spawned that are still working are **cancelled**: they end `failed` with `error` "cancelled: the agent that spawned it ended", and the agent's own run is finished **after** them, so the records are in order (a parent never finishes before its sub-agents). When the server stops, every agent is interrupted as before ("interrupted: the server stopped while the agent worked"), and a run left `running` by a dead process is marked `failed` when the server starts again.

## Messages

### Who can message whom

**Only siblings**, the direct children of one parent. A message goes to a sibling found **by name among the other sub-agents of the sender's parent**; there is no other way to address an agent. So an agent cannot message:

- its **parent**, or any ancestor, or the **root** (the way to hand something up is its report; the way down is the task);
- its own **children** (they are the siblings of one another, not of it);
- a **cousin**: the child of a sibling, or the sibling of an ancestor;
- an agent of another research, or itself.

A root has no siblings, so it has no messaging tools. Every sub-agent has them, even an only child (it just has no one to write to). `send_message` to a name that is not a sibling answers with an error that lists the siblings the agent has. These rules are in `team.Send` (`internal/agent_research/team.go`), not in the message system, which only carries what it is given, so a different rule is a change there.

### Tool `list_siblings`

No input. It returns the siblings of the caller, the other sub-agents of its parent, which are exactly the agents it can message: one line each, `- <name> [working|ended|ended, failed]: <reason>`, or a text saying it has no siblings now (the parent may still spawn some). It is how a sub-agent finds out, when it needs it, whether someone else is on a part of the topic that is related to what it found, or can give it a fact, instead of knowing the team from the start. It reads the current state each time, so siblings spawned or ended since the last call show up. The state of the agent is `messaging`. The tool is only given to sub-agents.

### Tool `send_message`

Input: `{"to": "beta", "text": "..."}`: `to` is the name of a sibling, `text` at most 10000 characters. It puts the message in the sibling's inbox and **returns at once**; it never waits for the sibling to read it. Errors, returned to the model: no such sibling, yourself, the sibling has ended (its inbox is gone: the message is not delivered), or its inbox is full (64 unread messages).

### Tool `read_messages`

Input: `{"wait_seconds": 30}` (optional, default 0, at most 60). It returns the messages received since the last read, oldest first, one per block (`From <name> (run <id>): <text>`), or `No new messages.`. With `wait_seconds` and an empty inbox it waits up to that long for a message (the state is `waiting`) and returns it with any that came with it; it also stops when the agent is cancelled. Reading takes the messages out of the inbox: each is returned once.

See [Prompts](#prompts) for what the sub-agent is told about messaging: it may message, if it helps, and `list_siblings` is how it decides.

## Prompts

The prompts are not in the Go code: each is a Markdown file in `internal/agent/prompts/`, embedded in the binary (`go:embed`), and the package exposes them as strings (`prompts.Research`, `prompts.Spawn`, ...). The system prompt of an agent is made of parts joined by a blank line (`prompts.Join`):

| File | Part | Given to |
| ---- | ---- | -------- |
| `research.md` | the base: research the web, check claims, write the report | every research agent |
| `knowledge.md` | how to use `query` and `remember` | agents with the knowledge base |
| `spawn.md` | **when and how to spawn sub-agents** | agents with `spawn_subagent` |
| `subagent.md` | who it is (name), why it was spawned (reason), that its answer goes to its parent | sub-agents |
| `messaging.md` | that it may message its siblings, `list_siblings`, and when it is worth it | sub-agents |
| `web_search.md` | the system prompt of the call the `web_search` tool makes | not part of the agent's prompt |

`spawn.md` is the policy of the agent that starts a research. It says that the default is **not** to spawn (every sub-agent costs as much as a full agent) and that the agent decides, before it starts searching, whether spawning is really warranted: only if the topic splits into two or more independent parts, each needing research of its own (clearly more than a few searches), too much to do one after the other within its own steps; never for a narrow or factual question, a topic a few searches cover, a part that needs the result of another, or to double-check itself. If it does spawn it gives each sub-agent a reason (and does not spawn one it cannot write a reason for), uses as few as it needs (usually 2 to 4), gives self-contained, narrow, non-overlapping tasks that say what to bring back, starts them in one step, researches its own part meanwhile, then waits, compares the reports and writes its own.

`messaging.md` tells a sub-agent that messaging a sibling is an option, not a duty: it calls `list_siblings` (names, reasons, who is still working) when it has to decide whether someone can help it or needs something from it, and sends a message only when it saves work (hand over a finding, ask for one fact, say that it covers something), short and specific, without waiting for a reply unless it needs one. `subagent.md` is a template (`text/template`: the name and the reason); a sub-agent that can spawn is told to do so only if its own task is clearly too big.

### The message system

The message system is simple on purpose, and **replaceable**.

- **`Messenger`** (`internal/agent_research/messages.go`) is the interface of a message system. It works with run IDs only, and knows nothing of agents, names or siblings:
  - `Open(id)` and `Close(id)` make and remove the inbox of an agent (done when a sub-agent is spawned and when it ends);
  - `Send(ctx, message)` puts a `Message` (`From`, `FromName`, `To`, `Text`, `SentAt`) in the inbox of `message.To` without waiting; it fails with `ErrNoInbox` (no inbox: the agent ended) or `ErrInboxFull`;
  - `Receive(ctx, id, wait)` takes out the messages, waiting up to `wait` for the first one if there are none.
- **`SendMessage(ctx, messenger, message)`** is the function every agent sends through (it sets `SentAt`, then calls `Messenger.Send`). Nothing else calls `Send`, so the sending has one place.
- **`NewChanMessenger()`** is the one used: **Go channels**. The inbox of an agent is a buffered `chan Message` of 64 (`InboxSize`) in a map guarded by a lock; a send is a non-blocking channel send (a full inbox refuses instead of blocking the sender, so two agents cannot deadlock on each other); `Receive` drains the channel and, with a wait, blocks on it, a timer and the context. `Close` forgets the channel without closing it, so a late sender cannot panic. Everything is in memory in the server process: messages are **not stored** and are lost when the server stops (no agent survives a restart anyway: its run is marked `failed`).

To swap the message system (a queue, the database, a broker between processes), implement `Messenger` and set `Manager.Messenger` after `NewManager` and before starting any agent; the agents are not touched. A new implementation must be safe for concurrent use and keep the contract above (non-blocking `Send`, the two errors, `Receive` that waits).

## Example

A user starts a research on "the electric-vehicle battery market". The root (run 1) thinks, then spawns two sub-agents in one step:

```
spawn_subagent {"name": "chemistry", "reason": "chemistries and costs are a research of their own, separate from the industry side", "task": "What battery chemistries are in EVs today and next, with energy density and cost per kWh? Report figures and source URLs."}
spawn_subagent {"name": "market",    "reason": "market shares need their own search of recent industry reports", "task": "Who are the biggest EV battery makers and their market shares in the latest year? Report figures and source URLs."}
```

Both return at once, runs 2 and 3 start (`parent_id: 1`, `name: "chemistry"` / `"market"`), and `GET /v1/gb/research?parent_id=1` lists them while they work. The root calls `wait_subagents {}` (state `waiting`). Meanwhile `chemistry` finds that one chemistry dominates a maker's lineup, calls `list_siblings` (it sees `market` and its reason, so it knows who covers the makers) and then `send_message {"to": "market", "text": "LFP is now the majority of CATL's output: check its share"}`; `market` sees it with `read_messages` at its next step. When both end, `wait_subagents` returns their two reports, and the root writes its own, which is the report of run 1.

## Not included yet

Messages between agents that are not siblings (a parent and its sub-agents, cousins), storing the messages (a transcript of what the agents said), a message that tells an agent while it is in the middle of a model call (messages are read when the agent calls `read_messages`), cancelling one sub-agent from its parent, a cap on the cost of a whole tree of agents, a per-user or server-wide setting that turns sub-agents off, and counting the cost of the sub-agents in their parent's `cost_usd`. See the [roadmap](roadmap/ROADMAP.md).
