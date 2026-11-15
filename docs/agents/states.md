# States

A **state** is one stage of what a research agent is doing: "it is asking the model what to do", "it is reading this page", "it is writing the report". The states of a run are its **history**, kept in the order the agent entered them, so you can see how a research went from start to end.

States are about *activity*. They are not about the research plan: the sub-questions the investigative loop works through are the **frontier questions** ([deep-research-loop.md](deep-research-loop.md#the-question-frontier)), with their own statuses. The two only meet in the `selecting` and `delegating` states, which say which question the loop picked or handed out.

This page explains what a state is, which states exist, how they are stored and how to read them. The run they belong to (its `status`, the report, the usage) is in [research.md](research.md#runs-and-states), and the agent's loop in [deep-research-agent.md](../deep-research-agent.md).

## What a state holds

Each state is a row of `gb_agent_research_states`:

| Column | Meaning |
| ------ | ------- |
| `seq` | 1, 2, 3, ... in the order the state was entered. It is unique within the run. |
| `state` | The name of the stage, such as `thinking` or `searching` (the list is [below](#the-states)). |
| `step` | The model call the state belongs to (`0` for `starting`, `completed` and `failed`). |
| `detail` | What the state is about: the search query, the URL being read, `step 3 of 20`, the question ids of a delegation, and so on. |
| `error` | Set when the step failed without failing the run, for example a page that could not be read (`example.com/paper: status 404`). The model is told and the run goes on. |
| `tokens` | Tokens spent by the web search of the state (non-zero only for `searching`, and only with providers that report them). |
| `started_at`, `finished_at` | Millisecond timestamps. `finished_at` is null for the state the run is in now. |

The table is **append-only**. A state is entered, then ended (`finished_at` set); it is never rewritten. The current state of a run is always its last row, and the run's `state` field mirrors it.

Each run has its own states. A sub-agent of the investigative loop is a run of its own, so it has its own states, and it is listed with its parent (see [subagents.md](subagents.md)).

## The states

### Common to all loops

| `state` | What the agent is doing | `detail` |
| ------- | ----------------------- | -------- |
| `starting` | The run is recorded; no model call has been made yet. | |
| `thinking` | A model call is running: the model decides what to do next. | `step 3 of 20` |
| `searching` | The `web_search` tool is running. | the query |
| `reading` | The `fetch_url` tool is running. | the URL |
| `querying` | The `query` tool is running (the knowledge base). | the question |
| `remembering` | The `remember` tool is running. | `async: ` or `sync: `, then the start of the text |
| `using_tool` | Any other tool is running. | the tool's name |
| `writing_report` | The last model call, which may not use tools, is running. Its text is the report. | `step 20 of 20, the last: ...` |
| `completed` | The run ended with a report. | |
| `failed` | The run ended with an error. | |
| `stopped` | The run was stopped before it ended (from the admin UI). | |

The usual path of a simple run is `starting`, then `thinking` and a tool state (`searching`, `reading`, `querying`, `remembering` or `using_tool`) as many times as the model needs, then `thinking` again and `completed`. When the agent uses all its steps, the last model call is `writing_report` instead, so a run always ends with a report. A run that fails has the state it failed in with the error, followed by a `failed` state that carries the error too.

### Investigative loop

A run of an agent with `loop = "investigative"` ([agent-defs.md](agent-defs.md#loop-and-investigate-the-investigative-loop)) adds these states, which describe the loop's own steps ([deep-research-loop.md](deep-research-loop.md)):

| `state` | What the loop is doing | `detail` |
| ------- | ---------------------- | -------- |
| `planning` | The first level of questions is being asked for. | |
| `selecting` | The loop picks the next question of the frontier. No model call. | `frontier_next` |
| `extracting` | Forced extraction: the notes are the only tools available. | |
| `verifying` | A claim of the current question is not verified and has no challenge yet. | |
| `challenging` | A challenge (an adversarial search for a claim) is open. | |
| `consolidating` | The notes are over `notes_max_tokens` and are being condensed. | |
| `delegating` | Independent questions have been handed to workers (sub-agents). | the question ids |
| `waiting` | The loop waits for its workers. | the question ids |
| `synthesizing` | The report is written from the notes, without search tools. | |

The `detail` of a turn of this loop is `turn 7 of 60, question q_003`. A tool call inside the loop keeps its usual state (`searching`, `reading`, ...).

### Other

- `spawning`: the `spawn_subagent` tool of the simple loop was removed in 0.189.0. Runs made before then still have these rows; no new run enters it.
- `importing`: only for a run in Modal (`runner` `modal`). The container has ended and the server loads what the agent learned into its databases. An `error` on this state says why the import failed; the run keeps its report.

## Reading the states

### REST and MCP

`GET /v1/gb/research/{id}` returns the run with its `states`, in the order they were entered:

```json
{"id": 7, "status": "completed", "state": "completed", "steps": 5,
 "states": [
   {"seq": 1, "state": "starting", "step": 0, "detail": "", "error": ""},
   {"seq": 2, "state": "thinking", "step": 1, "detail": "step 1 of 20", "error": ""},
   {"seq": 3, "state": "searching", "step": 1, "detail": "solid-state battery energy density 2025", "error": ""},
   {"seq": 4, "state": "thinking", "step": 2, "detail": "step 2 of 20", "error": ""},
   {"seq": 5, "state": "reading", "step": 2, "detail": "https://example.com/paper", "error": "example.com/paper: status 404"}
 ]}
```

The same run is returned by the `research_get` MCP tool. The timestamps and the rest of the states are omitted here. `GET /v1/gb/research/{id}/stream` pushes the run as Server-Sent Events, and each snapshot carries the states too. The admin UI shows them in the **States** section of a run's Details modal.

### CLI

`gligabao research get ID` prints the run with every state it went through. Add `--raw` to print only the report.

### Progress events

The standalone binary [`gligabao_agent`](../deep-research-agent.md#the-standalone-binary-gligabao_agent) prints one JSON line each time an agent enters a state:

```json
{"event":"state","agent":"main","state_seq":3,"state":"searching","step":1,"detail":"solid-state battery energy density 2025","error":""}
```

`agent` is `main` or the name of a sub-agent, and `state_seq` numbers the states of that agent from 1. When a state ends with an error, the same `state_seq` is written once more with the `error` set.

## How the states are recorded

The states are written by the manager of the research (`internal/agent_research/`). The agent reports each model call and tool run to an observer, and the manager turns them into state rows. The names are constants in `internal/agent_research/core/core.go`, and the investigative loop's names are in `internal/agent_research/investigate_core/loop_state.go`. Both tables are defined in `internal/agent_research/db/sqlite/sql/schema.sql` and `internal/agent_research/db/postgres/sql/schema.sql`.

A run of the Modal runner records the states its container reports, and the sub-agents' states inside it are imported with the sub-agent's name in front of the detail ([research.md](research.md#what-comes-back-the-import)).

## States and frontier questions, side by side

| | States | Frontier questions |
| - | ------ | ------------------ |
| Question answered | What is the agent doing, and what did it do? | What still has to be found out? |
| Structure | One ordered, append-only timeline per run | A graph of sub-questions, with parents and dependencies |
| Exists for | Every run | Investigative-loop runs only |
| Table | `gb_agent_research_states` | `gb_agent_frontier_questions` |
| Status values | The state names above | `pending`, `in_progress`, `resolved`, `blocked`, `unresolvable`, `pruned` |

A question's `status` is a different enum from a run's state, even where the words look alike.
