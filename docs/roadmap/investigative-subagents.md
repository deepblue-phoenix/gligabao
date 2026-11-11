# Plan: sub-agents for the investigative loop (fan-out / fan-in over the frontier)

**Status:** implemented in 0.189.0 (see [subagents.md](../agents/subagents.md), the page of the feature as built; this page is the plan it followed, with the open questions: the answers taken are code-only delegation, the lean worker, worker cost in the supervisor's breakers with a share per worker, a notes slice of the answers and claims of the dependencies and ancestors, started workers always finish in a fan-out, `UNVERIFIED` always, and reduction in question-id order). Left out for later: the contradiction path of the reducer (a worker's contradicting source is a `contradicts` link), the optional `assigned_run_id` column, restart recovery of delegated questions, `run_code` in workers. Related: [deep-research-loop.md](../agents/deep-research-loop.md#parallelism-and-sub-agents) (the design's parallelism section, which this plan makes concrete), [subagents.md](../agents/subagents.md) (the existing sub-agents of the simple loop), [agent-defs.md](../agents/agent-defs.md#spawn-sub-agents).

## Goal

An agent with `loop = "investigative"` can run independent questions of its frontier in **sub-agents**. Sub-agents become a capability of the investigative loop **only**: the model-driven sub-agents of the simple loop (`spawn_subagent`, `wait_subagents`, `[spawn]`) are removed (see [Removal from the simple loop](#removal-from-the-simple-loop)). The sub-agents are ephemeral workers with a clean context, a scoped mandate and their own tool loop, that return a **distilled result** (claims with sources, not raw pages). The supervisor (the investigative loop) keeps the frontier, the notes and the claim ledger; workers never touch them. Benefits: the supervisor's context stays small (the raw search/fetch turns of a question never enter it), independent questions run in parallel (wall clock), and a worker may have different tools than the supervisor.

## Today

- The investigative loop (`internal/agent_research/investigate/loop.go`, pure core in `investigate_core/`) is strictly serial: `selectNext` picks one question (`frontier_next`), the model works on it for several turns (search, fetch, extract into the notes, verify, challenge), then it is resolved.
- Sub-agents exist only for the simple loop (`internal/agent/team.go`, `internal/agent_research/team.go`: `Manager.spawn`, `family`, `tree`, `member`). Validation rejects `[spawn] enabled` with `loop = "investigative"` (`agent_defs/validate.go`), and `researcher.go` builds an investigative agent without team tools. This plan inverts that: the simple loop loses the capability and the investigative loop gets it, in a different form (code-driven workers).
- The design page's "Parallelism and sub-agents" says sub-agents would **share** the run's frontier, notes and ledger (atomic claim of a question, serialized note appends). This plan deliberately does **not** do that: it follows the centralized-frontier pattern below, which needs no concurrent writers.

## Design decisions

1. **Supervisor-worker, centralized frontier.** Only the supervisor writes the frontier, notes, claims and challenges. A worker returns a structured result; the supervisor's **reducer** (pure code) turns it into effects. No sub-agent recursion: workers get no spawn tool (so `MaxSubagentDepth` is not a concern for them).
2. **The code delegates, not the model.** Consistent with the design ("everything that decides whether the loop continues is code"): selection of the questions to delegate is a pure function of the frontier (topological: pending, every dependency resolved, not blocked, highest priority first, up to `max_parallel`). The model is not given a `spawn_subagent` tool in the investigative loop. (Alternative in open questions.)
3. **Workers are lean, not full investigations.** A worker is not a nested investigative run (no own frontier, no phases, no gate). It is a bounded tool loop (like the simple loop, `agent.Agent`) on one question, with: web/knowledge tools (and `run_code` if enabled), a minimal system prompt, and a final **distillation turn**. This keeps cost bounded and the result shape uniform.
4. **Strict 1-to-1 first.** One worker per question. Topic-cluster batching (2-4 coupled questions in one worker) and perspective/persona delegation are later options (see Later).
5. **Distill on exit.** A worker never returns raw tool output. It ends by calling `submit_findings` (the only way to end), a tool whose input is the result schema below; the worker's own transcript is stored (trajectory, run row) but not given to the supervisor.
6. **Costs and limits are shared.** Worker runs are rows in `gb_agent_research_runs` with `parent_id` like today's sub-agents, count in the research `tree` (count and cost cap), and their cost is added to the supervisor's `LoopState` spending so the existing breakers (`max_cost_usd`, `max_total_tokens`) see it. A worker is cut by the tree cost cap like any agent.

## The worker contract

Mandate given to the worker (rendered by a pure function in `investigate_core`, e.g. `WorkerBrief(st, qid)`), nothing else:

- the question, its `rationale`, `validation_criteria` and `target_sources`, and the attempts already logged (queries to avoid);
- a **small slice of the notes**: the objective, the claims and sources linked to the question's ancestors/dependencies (its resolved dependencies' answers), capped by a token budget (`worker_context_tokens`);
- the rules it must follow: search for both confirming and refuting evidence for a hypothesis, give sources with URLs, state independence of sources, no speculation.

Result (`submit_findings` input; validated by a pure function, errors go back to the worker for one repair turn):

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

Bounds: answer, number of claims, number of gaps and total size are capped (a worker that exceeds them is told to compress, once).

## The reducer (supervisor side, pure)

`Reduce(state, questionID, result) -> (state, transitions)` in `investigate_core` (new file `delegate.go`), using the existing pure functions so every gate still applies:

1. Sources -> `AddSource` (dedup by canonical URL); claims -> `Append` into the notes (Claims/Datapoints), with `Support`/links, stance and `independent` flags. Claims enter as `UNVERIFIED`: `VERIFIED` is still only reached through `CanVerify` (independent sources, survived challenges), so a worker cannot self-verify.
2. Attempts -> `AddAttempt` on the question (the supervisor's attempt log stays complete).
3. `discovered_gaps` -> `Add` as children of the question with `Actor = code`, `CausedBy = question id`; the existing depth cap, `MaxQuestions`, duplicate and scope checks decide (a rejected gap goes to the notes as an open gap, like `ErrDepthCap` does today).
4. The question: `answered` -> resolved through `checkResolve` (answer, claim ids, validation criteria, the challenge rule for shallow questions); if a check fails the question stays `in_progress` for the supervisor model to finish (it has the distilled claims in the notes, so it can challenge/resolve in a normal turn). `partial` -> attempts count and the question returns to `pending` (retried, or `unresolvable` by `MaxAttempts`). `unresolvable` -> `StatusUnresolvable` with the worker's note as the logged assumption.
5. Contradictions with existing claims -> the existing `checkContradiction` / refute path (spawns the high-priority question).

The challenge step (adversarial verification) stays with the supervisor, because it needs the claim ledger. A later variant can delegate challenges to workers (`TurnChallenge` as a worker task).

## Changes in the loop

- `loop_state.go` / `prompt.go`: new turn kind or step `TurnDelegate`: not a model call. In `step`, in exploration, when `max_parallel > 1` and `SelectBatch(st, max_parallel)` returns 2 or more questions, the loop runs the **fan-out** instead of `selectNext`:
  1. mark each selected question `in_progress` (transition `selected`, actor code; one effect list; a new column/field `assigned_run_id` is optional, see Schema);
  2. spawn the workers concurrently (`errgroup`-like; one goroutine each, bounded by `max_parallel`);
  3. wait for all (fan-in), with the run's context: stopping the run cancels the workers;
  4. reduce each result in question-id order (deterministic), in one `Apply` per result;
  5. call the usual update (`Reblock`, eviction) and go on. With 1 selectable question the loop works as today (the supervisor works on it itself), unless `delegate_all = true`.
- Hooks: `Entered(turn, "delegating", "q_005,q_007")` and `Entered(turn, "waiting", ...)`, so the run's states show it (`gb_agent_research_states` gets `delegating`; `waiting` exists).
- `investigate.Config` gets a `Delegate` field: an interface `Workers` with `Run(ctx, brief WorkerBrief) (WorkerResult, WorkerUsage, error)`, implemented in `agent_research` (like `External`), so `investigate` stays testable with a fake. Worker usage is `Spend`-ed into `LoopState`.
- Breakers: before fan-out, the number of workers is cut so that the worst case (max steps x batch) does not pass the iteration/token/cost budget minus the synthesis reserve; if nothing fits, the loop works serially. A breaker firing during a fan-out lets the started workers finish or be cancelled (decision in open questions), then moves to synthesis as today.
- Failure: a worker that errors, times out or returns an invalid result twice does **not** fail the run: its question goes back to `pending` with an attempt logged (`outcome: worker failed: ...`); after `MaxAttempts` it is `unresolvable`.
- Synthesis phase stays single-agent. A reopen in synthesis goes back to exploration and may fan out again.

## Wiring in `agent_research`

- New file `internal/agent_research/workers.go`: implements `investigate.Workers` on top of `Manager.spawn`-like code: creates a child run (`parent_id`, `name = q_005`, `reason = "question q_005"`, `topic` = the brief), reserves a slot in `tree`, runs the worker agent on its own goroutine/HTTP client (existing `researcher` machinery), waits on `member.done`, returns the parsed result. Reuse `tree`, `member`, `family`, cost-cap handling and the trajectory merge (`member.traj`) rather than a second mechanism.
- The worker agent is built from a **worker definition**: a role/definition (`[investigate.workers]` in the agent definition, or `agent = "..."`) with its own `tools`, `search`, `knowledge`, `model` and `max_steps`; default = the supervisor's tools and model, `max_steps` 12. Tool specialization (academic/benchmark/news profiles) is then by `target_sources` -> profile mapping (see Later).
- Worker loop = `agent.Agent` with `submit_findings` as terminal tool (new file `internal/agent/findings.go`, tool + schema + validation, pure), last step forced to it (`tool_choice`), like `finalize_report` in the investigative loop.
- The model-facing tools (`spawn_subagent`, `wait_subagents`), roles and `on_start` do not exist for any loop any more; the investigative loop's workers are started only by the loop's code. The low-level machinery that is kept is what workers need: `Manager.spawn` (trimmed to a worker start, no `role`/`name` from the model), `tree` (count and cost cap), `member`/`family`, `kidCtx` cancellation, trajectory merge, the run columns `parent_id`/`name`/`reason`.

## Removal from the simple loop

The capability is removed from `loop = "simple"` (a breaking change: minor version bump, CHANGELOG `### Removed`). Do this as the first commit, before the new feature, so the two do not coexist:

- **Tools and prompts:** delete `internal/agent/team.go` (`Team`, `TeamTools`, `AddTeamTools`, `spawnTool`, `waitTool`, `ValidSubagentName` moves to the worker code if still needed) and `team_test.go`; delete `prompts/spawn.md`, `spawn_roles.md`, `subagent.md` and their loaders/tests in `prompts.go`.
- **Agent definitions** (`internal/agent/agent_defs`): remove the `[spawn]` table (`Spawn`, `Role`, `Trigger*`, `SpawnRoles`, `on_start` templates, `MaxDepth/MaxPerAgent/MaxPerTree`, `Def.Role`, role-cycle validation, `MaxSubagent*` and `DefaultSubagentSteps` constants, the `spawn.md` file of a bundle). A file that still has `[spawn]` is a **load error with a clear message** (`spawn: sub-agents of the simple loop were removed; see investigate.max_parallel`), not silently ignored. Migrate the bundled definition `build/agent_defs/news_scanner.toml` (it uses roles) and the built-in `default` (free-form spawn) to work without it; update `agent_defs_test.go`, `example_test.go` and the `agent_research` agent-def tests.
- **Runtime** (`internal/agent_research`): remove `team{}` (the `agent.Team` adapter), `canSpawn`, `teamTools`, `startRoles` and its note, the `ToolSpawnSubagent`/`ToolWaitSubagents` state mapping in `researcher.go`, role handling in `childDef`, and `team_test.go` cases for model-driven spawn. The simple `researcher` has no `kids` any more; `kids`, `kidCtx` and `endKids` stay, used by the investigative loop's workers.
- **States:** drop `spawning` (the new loop uses `delegating`); `waiting` stays (fan-in). Existing rows with the old states must still render in the admin UI and CLI.
- **Data kept:** old runs of sub-agents stay readable (`parent_id`, `name`, `reason`, `GET /v1/gb/research?parent_id=`, the admin UI tree and its children counts, trajectory merging). No schema change and no data migration.
- **Other surfaces:** check and update `internal/api/research` and `mcp` (request/response fields that mention roles or spawn), `cli/research.go`, `admin_ui/src/research.ts` (spawn-specific labels; the children view is reused for workers), the modal/sidecar remote path (`remote.go`, `sidecar/ai_train/ai_train_core/agent_run.py`: spawn-related fields, if any, are dropped), and `cmd/gligabao_agent`.
- **Docs:** rewrite `docs/agents/subagents.md` as the page of the investigative workers (it is currently the simple loop's page; keep the tracking and limits parts that stay true), remove the `[spawn]` section of `agent-defs.md` and the roles text, fix `research.md`, `deep-research-agent.md` and the roadmap row that mentions sub-agents (`Research agents: cancel, stream, memory, sub-agents`).
- The simple loop is otherwise unchanged. An agent that needs parallel decomposition uses `loop = "investigative"` with `max_parallel > 1`.

## Configuration

In the agent definition (`[investigate]` table, also overridable per request like the other knobs, `Investigate.Merge`):

| Knob | Default | Meaning |
| ---- | ------- | ------- |
| `max_parallel` | 1 | most workers at once; 1 = no delegation (today's behaviour, so nothing changes unless asked) |
| `worker_max_steps` | 12 | model calls of a worker (ceiling 40, as `MaxMaxSteps`) |
| `worker_context_tokens` | 1500 | cap of the notes slice in the brief |
| `max_workers` | 20 | workers in the whole run (also counts in the tree's `MaxSubagentsPerTree` unless raised for this loop; decision below) |
| `min_priority_to_delegate` | 0 | do not delegate trivial questions |

Validation (`agent_defs/validate.go`): `max_parallel > 1` needs `loop = "investigative"` (the old `[spawn]` table is a load error with every loop, see the removal section); worker ceilings cannot be raised by a file, same rule as `spawn` limits; `runner = "modal"` still unsupported.

## Schema

- No new table is required: workers are `gb_agent_research_runs` rows (`parent_id`, `name`, `reason`).
- Optional: `gb_agent_frontier_questions.assigned_run_id` (nullable, the worker's run) to show in the admin UI which worker took which question, and to recover after a server restart (a question `in_progress` with an assigned run that is no longer working goes back to `pending`). If added: both `schema.sql` files, upgrade file, `docs/listings/db-tables.md`.
- New transition trigger `delegated` (actor `code`) in `investigate_core` (closed vocabulary), and state `delegating`.

## Testing

- Pure, no model/DB (`purity_test.go` must still pass): `SelectBatch` (dependencies, blocked, priority order, bound, depth), `WorkerBrief` (slice and size cap), `ValidateFindings`, `Reduce` (each status; a claim cannot become VERIFIED; gaps over the depth cap; duplicates; contradiction path).
- Loop test with fake `Workers` in `investigate/loop_test.go`: fan-out of 3 questions, results reduced in order; a failing worker; a breaker mid-fan-out; cancel; `max_parallel = 1` equals today's transcript (regression).
- `agent_research` test with a fake model: worker runs are rows with `parent_id`, cost counted in the tree cap and in the supervisor's totals, trajectories merged.
- An eval case comparing serial vs parallel on the same objective (cost, tokens in the supervisor context, report quality) before turning it on by default.

## Steps (suggested order, each one a reviewable commit)

0. **Remove sub-agents from the simple loop** (section above); tests pass with the capability gone; leftover references grepped (`spawn`, `subagent`, `Team`, `roles`).
1. Pure core: `Findings` type + validation, `SelectBatch`, `WorkerBrief`, `Reduce`, trigger `delegated`; tests.
2. Worker agent: `submit_findings` tool and the terminal-tool loop in `internal/agent`; tests with a fake model.
3. `investigate.Workers` interface and the fan-out/fan-in step in `loop.go` (fake workers); breakers and failure handling; states/hooks.
4. `agent_research/workers.go` on top of `Manager.spawn`/`tree`; usage and trajectory merging; config knobs and validation; restart recovery.
5. Docs (`docs/agents/deep-research-loop.md` parallelism section, `agent-defs.md`, `subagents.md`, `research.md`, `docs/listings/db-tables.md` if schema), CHANGELOG + VERSION (minor), admin UI (workers per question in the investigation view), then `update gligabao docs`.
6. Eval, then decide the default of `max_parallel`.

## Later (not in this plan)

- Topic-cluster delegation (2-4 coupled questions per worker, to avoid scraping the same documentation twice) and perspective-based delegation (STORM personas, each given a partition of the frontier).
- Worker profiles by question type (`target_sources` -> academic / sandbox / news profile with different tools).
- Workers for challenges (adversarial queries run in an isolated context).
- Hierarchical nesting (a worker as sub-supervisor of a subtree): needs its own budget governor; the centralized frontier is kept until then.
- A worker that streams partial findings so the supervisor can start dependants earlier.

## Open questions for review

0. *(Decided)* Sub-agents live only in the investigative loop; the simple loop loses them. Still to confirm: is a breaking removal acceptable with no deprecation period (a `[spawn]` file fails to load), or should `[spawn]` be accepted and ignored with a warning for one release?
1. **Who delegates:** code only (this plan), or also a model tool `delegate_question(question_id)` for the supervisor to hand off a question it judges self-contained? Code-only is simpler and deterministic; the tool gives the model control.
2. **Worker shape:** the lean tool loop (this plan), or a full nested investigative run for hard questions (heavier, gives workers claims/challenges, but 3 levels of state)?
3. **Budget:** should worker cost count in the supervisor's breakers (this plan: yes), and should `max_workers` be separate from the tree's 10-sub-agent ceiling (a frontier of 40 questions would hit 10 quickly)?
4. **Notes slice:** how much context a worker gets (only dependency answers, or also claims that mention the same entities)? Too little repeats work; too much brings back the bloat.
5. **Breaker mid-fan-out:** let started workers finish (their cost is spent anyway, results are useful) or cancel at once?
6. **Result trust:** `UNVERIFIED` always (this plan) means the supervisor still spends turns verifying and challenging; is that the right split, or may a worker's claims with 2 independent sources start as `VERIFIED` pending a challenge?
7. **Reproducibility:** results are reduced in question-id order, but wall-clock completion order differs; is deterministic reduction order enough, given model nondeterminism anyway?
