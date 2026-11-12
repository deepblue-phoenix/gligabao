# Sub-agents

An agent that runs the [investigative loop](deep-research-loop.md) (`loop = "investigative"`) can run independent questions of its frontier in **sub-agents**, which this page calls **workers**: ephemeral research agents with a clean context, one question each, their own tool loop and a **distilled result** (claims with sources, not raw pages). The loop itself is the **supervisor**: it keeps the frontier, the notes and the claim ledger, and the workers never touch them. The supervisor's context stays small (the raw searches and pages of a question never enter it), independent questions run in parallel, and the workers cost what their model calls and searches cost.

Sub-agents belong to the investigative loop **only**. The model-driven sub-agents of the simple loop (the tools `spawn_subagent` and `wait_subagents`, the `[spawn]` table, roles) were removed in 0.189.0: a definition that still has `[spawn]` does not load (`spawn: sub-agents of the simple loop were removed; set loop = "investigative" and investigate.max_parallel ...`). An agent that needs parallel decomposition uses the investigative loop with `max_parallel` above 1. The agent itself (its loops, its web and knowledge-base tools) is in [deep-research-agent.md](../deep-research-agent.md); the job around it (runs, states, REST, CLI) is in [research.md](research.md).

Code: `internal/agent_research/investigate_core/delegate.go` (the pure side: which questions go, the brief, the findings, the reducer), `internal/agent_research/investigate/delegate.go` (the fan-out and fan-in of the loop), `internal/agent_research/workers.go` (starting a worker as a run, the limits) and `internal/agent/findings.go` (the `submit_findings` tool); the prompt of a worker is `internal/agent/prompts/worker.md`.

It is **off by default**: `max_parallel` is 1, and then nothing changes. The [limits](#limits) bound what it can cost.

## Concepts

- **Supervisor / worker.** The investigative loop of a research is the **supervisor**; the agent it starts for a question is a **worker**. A research a user starts (`POST /v1/gb/research`) is a **root**: it has no parent.
- **The code delegates, not the model.** Which questions go to workers is a pure function of the frontier; the model has no tool to start a worker. This is the rule of the loop: everything that decides whether it goes on is code.
- **One worker per question.** A worker is not a nested investigation: it has no frontier, no phases and no gate. It is a bounded tool loop (like the simple loop) on one question, with the web tools, the knowledge base if the definition gives it one, a minimal system prompt, and a last step that is forced to call `submit_findings`.
- **Name and reason.** A worker's name is the id of its question (`q_005`; `q_005-2` if that question goes to a worker again) and its reason is `question q_005: <the question>`; both are stored with the run.

```
root (research started by the user, loop = investigative, max_parallel = 3)
├── q_002   ┐ workers of one fan-out, at the same time
├── q_003   │
├── q_004   ┘
└── q_003-2   the same question again, after a partial result
```

## The fan-out

In the exploration phase, when the objective has sub-questions and none of them is in progress, the loop looks at the frontier before it selects a question itself. If `max_parallel` is above 1 and **two or more** questions can go, it hands them over **without a model call** of its own:

1. **Select.** `SelectBatch`: the pending questions (not the root) whose dependencies are all resolved, that have validation criteria and a priority of at least `min_priority_to_delegate`, highest priority first, up to `max_parallel` (and up to what is left of `max_workers`). With one question, or a budget that is nearly spent (see [limits](#limits)), the loop works on the question itself, as without workers.
2. **Mark.** The questions become `in_progress` with the transition trigger `delegated` (actor `code`), in one list of effects. The run's state is `delegating` (detail: the question ids).
3. **Run.** One worker per question, each on its own goroutine, with the run's context: stopping the research stops its workers. The state is `waiting` while the loop waits for all of them (fan-in).
4. **Reduce.** The outcomes are reduced in **question-id order** (the same findings give the same state, whatever the order they came in), one change of the state per worker. Then the loop goes on: it checks the breakers and either fans out again or works on a question itself.

The supervisor's model is called only for planning, the questions it has to finish itself, the challenges and the synthesis. The `steps` of the supervisor's run row count those calls only; what the workers spent is in their own runs (the supervisor's breakers do see it, see [limits](#limits)).

## What a worker is told: the brief

The brief is the whole input of the worker (`WorkerBrief`, a pure function, cut by `worker_context_tokens`):

- the objective, the question with its `rationale`, `validation_criteria` and `target_sources`, and, for a hypothesis, the instruction to look for evidence for **and against**;
- the **attempts already logged** for the question (queries to avoid; a retry shows what the earlier workers found);
- a **slice of the notes**: the answers of the questions it depends on and of its ancestors, and the claims of the notes about those questions.

Its system prompt (`worker.md`) tells it to work on this question only, to take only what the pages say, to give a source URL for every claim, to list what it finds and does not follow as a gap, and to end with `submit_findings`.

## What a worker hands back: the findings

`submit_findings` is the only way a worker ends. Its input:

```json
{
  "status": "answered | partial | unresolvable",
  "answer": "short answer to the question, citing claim keys",
  "claims": [{"key": "c1", "text": "...", "kind": "claim|datapoint", "value": "...", "unit": "...",
              "sources": [{"url": "...", "title": "...", "stance": "supports|contradicts", "independent": true, "quote": "..."}]}],
  "queries": [{"query": "...", "tool": "web_search", "outcome": "..."}],
  "discovered_gaps": [{"question": "...", "rationale": "...", "priority": 0.6, "kind": "question|hypothesis"}],
  "notes": "caveats, dead ends (short)"
}
```

It is checked by a pure function (`ParseFindings`); the error goes back to the worker, which fixes it in a later step. The bounds: the answer at most 800 characters, 12 claims with at most 5 sources each, 5 gaps, 15 queries (more are cut), the notes 1000 characters, 14000 in all. A worker that answers in text, without calling the tool, is told to call it. The worker's own transcript is stored (its run, its states, its trajectory), but **not** given to the supervisor; the report of a worker's run is its findings as JSON.

## What the supervisor does with it: the reducer

`Reduce(state, question, findings)` in `investigate_core` turns the findings into the usual changes, with the checks of the loop's tools, so every gate still applies:

1. **Claims and sources.** Each source goes to the notes (one per URL, so the same page found by two workers is one source), each claim to `Unverified` (a datapoint to `Datapoints`; one whose value has spaces is recorded as a claim with the figure in its text), tied to the question, with its links, stance and `independent` flag. A worker can **never** verify a claim: `VERIFIED` is only reached through `CanVerify` (independent sources, survived challenges), by the supervisor. A claim that cannot be recorded is dropped.
2. **Attempts.** The queries are logged as attempts of the question (when it goes back to pending, only as many as leave room for the attempt the return itself logs, so that the attempt cap counts the tries and not the queries).
3. **Gaps.** Each `discovered_gaps` entry is added as a child of the question, by code (`actor = code`, `caused_by` the question). One that the depth cap, the question cap or the duplicate check rejects is written to the notes as an open gap, like the same rejection of a model's `add_question`.
4. **The question.**
   - `answered`: it is resolved if the checks of `resolve_question` pass (the cited claims exist, a hypothesis was searched for and against, a question of depth 0 or 1 has a closed challenge of its answer). If they do not (always the case at depth 1, and for a hypothesis, whose refuting searches are challenges), it **stays `in_progress`** with the worker's answer logged as an attempt, and the supervisor finishes it in its normal turns: it has the claims in the notes, so it challenges and resolves. The challenge step, the adversarial verification, stays with the supervisor because it needs the claim ledger.
   - `partial`: it goes back to `pending` (the retry of the loop, a `retried` transition) with the worker's notes as the outcome of a new attempt; at the attempt cap it becomes `unresolvable`.
   - `unresolvable`: it becomes `unresolvable` (trigger `attempts_exhausted`, actor `code`), with the worker's notes as the assumption, which is also in the notes' "Assumptions & gaps".

The final check of the loop still queues the claims that are `UNVERIFIED` behind resolved questions as questions to verify (`Verify claim c_001: ...`); these are questions like any other and may go to workers too.

**Failure.** A worker that errors, is stopped, runs out of steps without submitting, or hands in findings that stay invalid does **not** fail the research: its question goes back to `pending` with `worker failed: ...` logged as an attempt, and becomes `unresolvable` at the attempt cap.

## Tracking

A worker is tracked like every other agent. It is a run in `gb_agent_research_runs` with its own states (`gb_agent_research_states`), status, usage (steps, tokens, `cost_usd`), `report` and `error`, owned by the same user as its supervisor, so that it shows in the list and get endpoints, the CLI, the MCP tools and the admin UI like any run. What is new on a run:

| Field / column | Meaning |
| -------------- | ------- |
| `parent_id` | the run of the supervisor that started it; `null` for a research a user started |
| `name` | the id of its question, `q_005` (`q_005-2` for the next worker of that question); `""` for a root |
| `reason` | `question q_005: ...`; `""` for a root |

- `(parent_id, name)` is unique. Deleting a run deletes its workers (`ON DELETE CASCADE`).
- A worker runs on the **same model and dataset** as its supervisor, with the web and knowledge tools of the same definition (and the knowledge access it gives). Its `topic` is its brief and its `agent_name`, `agent_version` and `agent_hash` are those of the supervisor's definition.
- `GET /v1/gb/research?parent_id=<run id>` lists the workers of a run. Without it, the list has all runs, workers included; tell them apart by `parent_id`.
- Workers do not count in the number of researches a user may have working at once (3); the [limits](#limits) bound them.
- The states of the supervisor have two values for this page: `delegating` and `waiting` (see [research.md](research.md#runs-and-states)).
- The trajectories of the workers are merged into the supervisor's when trajectories are recorded (`Manager.UseTrajectories`), each step tagged with its agent (the question id), run and parent.
- **Old runs stay readable.** Runs made by the sub-agents of the simple loop (before 0.189.0) keep their `parent_id`, `name` and `reason`, their old states (`spawning`, `waiting`), and show in the list, the admin UI tree and the child counts like workers. There is no schema change and no data migration.

## Configuration

In the agent definition, the `[investigate]` table (also overridable per request, like the other knobs; they only take effect with `loop = "investigative"`):

| Knob | Default | Meaning |
| ---- | ------- | ------- |
| `max_parallel` | 1 | most workers at once, 1 to 8; 1 = no delegation (today's behaviour) |
| `worker_max_steps` | 12 | model calls of a worker, at most 40; its last call is forced to `submit_findings` |
| `worker_context_tokens` | 1500 | size of the slice of the notes in the brief, 100 to 8000 |
| `max_workers` | 20 | workers in the whole run, at most 40 (retries count) |
| `min_priority_to_delegate` | 0 | a question below this priority is not delegated |

```toml
loop = "investigative"
[investigate]
max_parallel = 3
worker_max_steps = 10
max_workers = 15
```

The ceilings cannot be raised by a file. `runner = "modal"` is still not supported for the investigative loop.

## Limits

- **`run_code`.** A worker gets the `run_code` tool too when its definition (the supervisor's) has [`[code]`](agent-defs.md#code-the-run_code-tool) enabled and the sandbox is installed; same sandbox and limits, `max_calls` counts per worker, and the worker's prompt gets a short code section. Its programs can call `web_search`, `fetch_page`, `kb_query` and `log` (through the worker's own tools); the host functions that read the supervisor's state (`notes_read`, `frontier_*`, `claims_list`) are not offered. Without the sandbox the worker runs without it (a warning is logged).
- **Workers**: `max_parallel` at once, `max_workers` in the whole run, `worker_max_steps` model calls each (and each call with the web searches it makes). Workers get no tool to start workers: there is one level.
- **Cost.** A worker's cost is its own (`cost_usd` of its run) and is **not** added to the supervisor's row, but it counts for the supervisor's breakers (`max_cost_usd`, `max_total_tokens`), and a [cost cap](agent-defs.md#limits-cost-caps) of the research counts all the agents together. Before each fan-out:
  - if exploration has already spent 60% of what the token or cost limit allows it (the limit minus the reserve of the synthesis), the loop does not delegate, so that the reserve stays for the report;
  - each worker is given a **share** of what exploration may still spend (`(exploration limit - spent) / workers`) as a cap of its own: when it reaches it, it makes its last call and hands in what it has (one model call can still pass the cap).
  When the cost cap of the research is reached, every agent of the tree stops at its next model call and no more workers are started.
- **When the supervisor ends**, whether it wrote its report, failed or was interrupted, its workers that are still working are cancelled (`failed`, `error` "cancelled: the agent that spawned it ended"), and the supervisor's run is finished after them, so the records are in order. Stopping a research ([research.md](research.md#admin-ui)) stops its workers too; stopping one worker (it ends `stopped`) does not stop the supervisor: that question goes back to the frontier. When the server stops, every agent is interrupted, and a run left `running` by a dead process is marked `failed` when the server starts again.

## Example

A user starts a research on "batteries" with an agent whose definition has `max_parallel = 3`. The supervisor plans three questions (one model call: `add_question` three times). The loop sees three questions that can go, marks them `in_progress` (`delegated`) and starts the workers `q_002`, `q_003` and `q_004` (runs 2, 3 and 4, `parent_id: 1`); the research is `delegating`, then `waiting`, and `GET /v1/gb/research?parent_id=1` lists them while they work. `q_002` hands in `answered`: its claims are in the notes as `UNVERIFIED`, but it is a question of depth 1, so it stays `in_progress`. `q_003` hands in `partial`: it goes back to `pending` with a new attempt. `q_004` hands in `answered`, one gap and a claim; it is at depth 2, so it is resolved, and the gap is a new question `q_005`. The supervisor then finishes `q_002` in its own turn (it challenges its answer and resolves it); the next fan-out takes `q_003` and `q_005`; and so on to the synthesis, which the supervisor writes alone.

## Not included yet

Delegating the **challenges** (adversarial queries in an isolated context), more than one question per worker (a topic cluster, to avoid reading the same documentation twice), perspective-based delegation (STORM personas), worker profiles by `target_sources` (academic, news, sandbox), workers that stream partial findings so that dependants start earlier, a worker that is a sub-supervisor of a subtree (it needs its own budget governor), a worker that tells the supervisor about a **contradiction** with an existing claim (the contradiction path of the tools; now a worker's contradicting source is a `contradicts` link), a model tool for the supervisor to hand a question over, the `modal` runner, cancelling one worker from its supervisor (an admin can stop any agent from the [admin UI](research.md#admin-ui)), a record of which worker took which question in the frontier tables (the worker's name is the question id), and counting a worker's cost in its supervisor's `cost_usd`. See the [roadmap](../roadmap/ROADMAP.md) and [the plan](../roadmap/investigative-subagents.md).
