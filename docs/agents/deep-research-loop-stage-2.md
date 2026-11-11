# Deep-research loop, stage 2: the investigative loop

**Status:** to implement, after [stage 1](deep-research-loop-stage-1.md). This is the second of two implementation stages of the [deep-research loop](deep-research-loop.md). It connects the state, storage and tools built in stage 1 to a model, and adds the loop, the gates, the breakers and the observability. The design page defines the semantics; this page says what to build, in what order, and how to know it is done.

Read the design page and stage 1 in full first. Where this page is narrower than the design, this page wins for this stage; where it seems to contradict the design, stop and raise it rather than choose.

## Goal

Make `loop = "investigative"` a working agent loop, end to end:

- the **exploration loop** runs the cycle (state, decide, act, observe, update) on the frontier, with the turn builder that keeps the prompt bounded;
- **forced extraction** and **context eviction** keep the history small;
- the **finalization gates** decide when exploration ends and when the report is accepted;
- the **synthesis loop** writes the report from the notes and the frontier, without searching;
- the **circuit-breakers** and the **synthesis reserve** guarantee that a report is always written, or the run ends with a named reason;
- the run records its phase, its stop reason, its states and its challenges, so that a run can be inspected afterwards.

The simple loop stays as it is and stays the default. `loop = "simple"` behaves exactly as before.

## Depends on

- Stage 1 is complete: the pure core, the tables, the store, the effect executor, the tool registry, the notes file and the limits struct.
- The existing research runtime (`internal/agent_research/researcher.go`, `team.go`, the model call path, the cost accounting, the trajectory writer). Read them before writing the loop; the investigative loop reuses the model call and the accounting, it does not copy them.

## Out of scope for this stage

- Parallel workers on the frontier (`max_parallel` stays 1; the sub-agent sharing of notes is a leftover task, see the design's [parallelism section](deep-research-loop.md#parallelism-and-sub-agents)).
- Notes in the DB or an object store.
- Semantic dedup of questions (the normalized string test of stage 1 is the only one).
- Automatic source independence beyond domain and the agent's declaration.
- Changes to the `web_search` server-side behaviour.

## Ground rules

- Functional style is mandatory (see `gligabao/AGENTS.md`). The turn builder, the gates, the breaker checks and the phase transitions are pure functions of the loop state. The model call, the tool execution and the DB are at the edge.
- Code decides whether the loop continues; the model decides content. A text-only turn is never an end: the loop reminds the model of the open frontier and continues.
- Schema changes follow the design's [Migrations](deep-research-loop.md#database-schema) rules: a new table is `CREATE TABLE IF NOT EXISTS` in both `schema.sql` files; a change to an existing table is an `ALTER` in the package's upgrade list (SQLite: only if the column is missing; PostgreSQL: `ADD COLUMN IF NOT EXISTS`), run on every start and also reflected in `schema.sql`. Update `docs/listings/db-tables.md` for every new table.
- Versioning: a new loop and knobs are a minor bump, with a `CHANGELOG.md` entry that states that `loop = "simple"` is still the default.

## Recommended layout

| Path | Content |
| ---- | ------- |
| `internal/agent_research/investigate/loop_state.go` | the pure loop state: phase, turn count, budgets used, stop reason, the window of recent turns, the uncommitted results |
| `internal/agent_research/investigate/prompt.go` | the pure turn builder: system prompt, tool definitions for the phase, task, notes, frontier summary, the last `keep_turns` turns, the reminders |
| `internal/agent_research/investigate/evict.go` | the pure eviction rule (what may be dropped, what must stay) |
| `internal/agent_research/investigate/gates.go` | the pure exploration gate and finalization gate |
| `internal/agent_research/investigate/breakers.go` | the pure breaker check and the reserve arithmetic |
| `internal/agent_research/investigate/loop.go` | the effect layer: runs the cycle, calls the model, runs tools, executes effects |
| `internal/agent_research/investigate/` (new tool files) | `finish_exploration` and `finalize_report` tool specs and handlers |
| `internal/agent_research/` | wiring: choose the loop from the agent definition / run options; the trajectory and the state rows |

## Work items

Do them in this order.

### 1. Loop state and the phases (pure)

`loop_state.go`. The value holds: the current phase (`exploration` or `synthesis`), the turn count, the tokens and cost used so far, the number of reopens used, the stop reason (empty while running), the window of recent turns, and the list of results not yet committed (see item 4).

Transitions, as pure functions:

- `Start` -> `exploration`, after the broad query expansion. The `planning` state is the phase where the root has no child yet: `frontier_next` is refused, the model is asked for the first level.
- `exploration` -> `synthesis` on a passing `finish_exploration`, on an exhausted frontier after the final check (item 7), or on a breaker that leaves the reserve (item 8).
- `synthesis` -> `exploration` on `finalize_report` only when it reopens a question, at most `max_reopens` times (default 1).
- `synthesis` -> end on a passing `finalize_report`, or a breaker that exhausts the reserve (stop reason `failed`).

Every transition is written to `gb_agent_research_states` by the effect layer, with the state values of the design (`planning`, `selecting`, `extracting`, `verifying`, `challenging`, `consolidating`, `synthesizing`).

### 2. Turn builder (pure)

`prompt.go`. Builds the messages of **one** turn from the loop state, and nothing else. The prompt contains, in this order:

1. the system prompt of the agent (from the agent definition) plus the loop's instructions for the phase;
2. the tool definitions of the phase (exploration tools, or the synthesis tools; in the forced-extraction turn only the notes tools);
3. the task (objective) as the first user message;
4. the scratchpad: the rendered `notes.md` and the frontier summary (stage 1's `Summary`), with the open challenges listed under it;
5. the last `keep_turns` turns (default 2), raw tool input and output.

Nothing older is included. Tests (no model): a long history produces the same prompt size as a short one of the same notes and frontier; the open challenges and the current question appear; the synthesis prompt has no search tool.

The current question block of the design's [prompt contents](deep-research-loop.md#prompt-contents) (criteria, attempted queries, rationale) is part of the prompt when the loop has selected a question.

### 3. Decide and act

The cycle of the design, executed by `loop.go`:

1. **State**: the turn builder (item 2) builds the request.
2. **Decide**: when the loop has no current question, call `frontier_next` (a deterministic step, not a model call, in the effect layer). The model is told which question it is, with its rationale, criteria and attempted queries.
3. **Act**: the model call. Its tool calls are executed one by one by the registry (stage 1 handlers) and the web tools (`web_search`, `fetch_page`, `query`, existing code).
4. **Observe** and **Update**: the handlers return effects; the effect executor runs them in one transaction (including the `gb_agent_notes_versions` row of a notes write); the notes file is written after commit.
5. **Restore**: at the start of each turn, if the notes file of the run is missing or unreadable, the loop writes it from `LatestNotesVersion` and adds a version with reason `restore`. The loop never starts a turn without a notes file.

After every update: run the reblocking of stage 1, the breaker check (item 8), and start the next turn. A turn with no tool call is a reminder turn (the open frontier and the open challenges are repeated in the next prompt), and it counts toward `max_iterations`.

Wire the logging of every attempt: each `web_search`, `fetch_page` or `query` made for a question inserts a row in `gb_agent_frontier_attempts` (with its outcome summary) through the effect executor, and a challenge's query links to that row (`attempt_id`).

### 4. Forced extraction and eviction (pure rule, effect wiring)

- `fetch_page` (the richer successor of `fetch_url`): rename the tool, keep the old name as an alias for the simple loop so existing agents work. Its result text is the page text, as before.
- Extraction threshold: a tool result above `extraction_threshold` characters (default 4000) applies to `fetch_page`, `query` and long search briefings. When one arrives, the loop does not hand it to the next turn as raw text. The next turn is the **forced extraction** turn: only the notes tools are offered, and the instruction is "extract what matters for question `<id>` from this result", with `notes_append` for claims, datapoints and sources.
- A result is **uncommitted** until the notes contain its useful content, or the loop records it as read with nothing relevant (a source with the note "read, nothing relevant"). Uncommitted results are never evicted.
- Eviction (`evict.go`): after a turn, the raw outputs older than the last `keep_turns` turns are dropped when committed. The attempt log keeps the query and a one-line outcome, so nothing else is needed. An evicted output leaves no stub in the history.
- Claims recorded by extraction start `UNVERIFIED`, linked to their source.

Tests: a page below the threshold is not forced; above it the next turn offers only notes tools; an uncommitted result survives eviction; a committed one does not.

### 5. Verification and challenges in the loop

- After extraction, a relevant `UNVERIFIED` claim is challenged (the design's [procedure](deep-research-loop.md#claims-and-cross-checking)): the prompt asks for a `challenge` call, then the search with the registered query, then `record_challenge_result`. The loop does not start `finish_exploration` with an open challenge.
- `VERIFIED` is set only through stage 1's `CanVerify`. The loop never sets it directly.
- A `refuted` outcome on a claim: stage 1's handler refutes the claim and spawns a high-priority question; the loop reopens every question whose `answer` cites that claim (code, using `Reopen`). The loop does this in the same effect list.
- `resolve_question` for depth 0 and 1 requires a closed answer challenge or a stored reason (stage 1 rule); the loop's prompt says so when the question is at those depths.

### 6. Consolidation

When `TokenEstimate(notes)` is above `notes_max_tokens` (default 6000), the next turn is a **consolidation turn**: the notes tools only, with the instruction to merge duplicates, shorten `Scratch`, and move settled claims to `Verified`. The turn ends when the estimate is under the bound or after one consolidation turn; a second one is only forced if the first did not reduce the size. The state `consolidating` is recorded.

### 7. Exploration gate and the end of exploration (pure gate)

`gates.go`, function `ExplorationGate(state) []Missing`, returns the list of what is missing (an empty list means pass). The checks are the design's [exploration gate](deep-research-loop.md#finalization-gate):

- the root is `resolved`, or `unresolvable` with a stated assumption;
- no actionable `pending` question with priority at or above `gate_priority` (default 0.5);
- at least `min_sources` distinct sources in the notes, from at least `min_domains` distinct domains (`min_sources` is scaled down by the run options for narrow tasks: the run option sets the value, the gate reads it);
- every `resolved` question cites at least one `VERIFIED` claim, or stores why `UNVERIFIED` is the best available;
- no unaddressed entry in `Contradictions`;
- no open challenge, and the `conclusion` has a closed challenge with outcome `survived`.

`finish_exploration` (tool): calls the gate; on failure returns the list of missing items as the tool error, so the model fixes them; on success moves the phase to `synthesis`.

Frontier exhaustion (no actionable question left): the loop runs the final check, which queues every `UNVERIFIED` claim relevant to a resolved question for verification (a question created by the loop, at the design's priority). If none is queued, exploration ends with stop reason `frontier_exhausted`.

The **conclusion challenge**: the conclusion is written in a few sentences in `Scratch` (a section the gate reads by a marker line), and the loop asks for one `conclusion` challenge before `finish_exploration`. If its outcome is `weakened` or `refuted`, the gate fails and the exploration continues on the questions the outcome opened.

### 8. Circuit-breakers and the synthesis reserve (pure check, effect layer)

`breakers.go`, function `CheckBreakers(loopState, limits) []Breaker` run before **every** model call. Breakers, from the design's [table](deep-research-loop.md#circuit-breakers):

| Breaker | Default | On fire, in exploration |
| ------- | ------- | ----------------------- |
| `max_iterations` (turns, exploration) | 60 | move to synthesis, note the cut |
| `max_total_tokens` (input + output of the run) | none, set by the run | move to synthesis |
| `max_cost_usd` (the existing cost cap) | the existing cap | move to synthesis |
| `max_attempts` per question | 4 | question becomes `unresolvable` (stage 1), not a loop stop |
| `max_questions` per run | 40 | no more nodes (stage 1 refuses `add_question`) |
| `max_depth` | 3 | not a loop stop (stage 1 refuses deeper nodes) |
| wall clock (`timeout`) | existing | ends the run as today |

Reserve: `synthesis_reserve` (default 0.15) of the iteration, token and cost limits is not spendable by exploration. Exploration breakers use the limit minus the reserve; synthesis breakers use the full limit. When a breaker fires in exploration, the loop moves to synthesis with a note in `Scratch` and in the report input: "the investigation was cut short by `breaker: <name>`", and the still-pending questions are listed as open gaps. The run is **not** failed.

If a breaker fires in synthesis (the reserve is exhausted), the run ends as `failed` with the breaker named in `error` (the same as today's agent that ends without a report).

The last synthesis turn is always forced: `tool_choice` set to `finalize_report`, replacing today's "last step has no tools".

Stop reasons written to `gb_agent_research_runs.stop_reason`: `finalized`, `frontier_exhausted`, `breaker:iterations`, `breaker:tokens`, `breaker:cost`, empty while running.

### 9. Synthesis loop

- Input: the objective, the notes (verified claims, datapoints, sources, contradictions, assumptions and gaps), the frontier summary with the `unresolvable` and `pruned` questions and their reasons. No search tool is offered.
- Tools: `notes_read`, `frontier_view`, `finalize_report(report)`, and `reopen_question` (only to send the run back to exploration, at most `max_reopens` times).
- `finalize_report` runs the finalization gate (pure, `gates.go`): the report cites only existing source ids (checked by ids in the text, not by text analysis of the claims); every `unresolvable` question appears under gaps; nothing in the report cites a `REFUTED` claim (checked by claim-id references). A failure is returned to the model as the list of problems.
- A passing `finalize_report` stores the report text in `gb_agent_research_runs.report`, sets `stop_reason = finalized` and ends the run.
- The report shape is the design's: summary first, then details, the source list as URLs, a section on gaps and assumptions, and the verification status of key claims. Every statement is traceable to claim and source ids, rendered as URLs.

### 10. Run options, agent-definition knobs and selection of the loop

- Add `loop` to the agent definition (`docs/agent-defs.md`): `simple` (default) or `investigative`. Validation rejects any other value with a clear error.
- Add the knobs of the design's [configuration](deep-research-loop.md#configuration) to the agent definition and to the run options (`internal/agent_research/options.go`), with the design's defaults. They take effect only when `loop = "investigative"`; setting them with `loop = "simple"` is an error, not a silent no-op.
- Run options override the agent definition, as today for the other limits.
- `max_iterations` replaces `max_steps` for the investigative loop only. The simple loop keeps its limit.

### 11. Persistence of the run, the states and the migrations

- Add the columns `phase` and `stop_reason` to `gb_agent_research_runs`. Both are changes to an existing table, so they follow the design's [Migrations](deep-research-loop.md#database-schema) rules:
  - `schema.sql` (both backends): `phase TEXT NOT NULL DEFAULT ''` and `stop_reason TEXT NOT NULL DEFAULT ''`. Keep them without a `CHECK`, so that a later value does not need a table rebuild (the vocabulary is enforced in Go, the constants of `loop_state.go`).
  - SQLite: two entries in the `Upgrade` list of `db/sqlite/schema.go` (`{"phase", "ALTER TABLE ... ADD COLUMN phase ..."}`, same for `stop_reason`); `Open` runs each only when the column is missing.
  - PostgreSQL: two lines in `db/postgres/upgrade_columns.sql` with `ADD COLUMN IF NOT EXISTS`.
  - Existing rows get `''`, which means "not an investigative run" (their `phase` and `stop_reason` are never read by the simple loop).
- Write one `gb_agent_research_states` row per state transition and per turn, with the question id in its `detail` as today. The new state values are not constrained, so they need no migration.
- The turn count is the existing `steps` of the run (it now counts turns for the investigative loop; state this in the changelog).
- The totals (`tool_calls`, tokens, cost) keep their meaning.
- Index: if a read needs one (for example runs by `stop_reason`), it goes in a file that runs after the upgrade, as `schema_subagent_indexes.sql` does, never before the column exists.

**Migration tests**, on both backends, each with a database made by the **previous** version's DDL:

- the server adds `phase` and `stop_reason` on start; old runs keep their values and get `''`;
- a second start changes nothing and does not fail;
- a fresh database has the columns from `schema.sql`, and the upgrade is a no-op;
- an investigative run's `phase` and `stop_reason` are written and read back;
- the simple loop still runs on the upgraded database (regression).

### 12. Observability

- The trajectory (ATIF) writer records the investigative states, with the question id, the phase and the stop reason.
- The query-level reads named in the design (the frontier tree from `parent_id`, the transition history, challenges per target and per strategy, outcome distribution, and the notes of a run at each write from `gb_agent_notes_versions`, joined to `gb_agent_research_states` by `state_seq`) must be possible with plain SQL on the tables. Add one example query per read to the design's observability section or to `docs/research.md`, and test that each runs on SQLite.
- The admin UI rendering of the frontier tree is **not** required by this stage; if it is not done, record it in `leftover_tasks/`.

### 13. Evals and the comparison

Compare the investigative loop with the simple loop on the same topics, as the design's delivery step 6 says:

- measure the cost, the number of verified claims, the gaps reported, the number of turns and the stop reasons;
- keep the eval topics in `build/evals_knowledge/` if they are knowledge evals (an eval file there needs its manifest `<name>.md`, see `gligabao/AGENTS.md`);
- do not change the default loop based on a single run; record the numbers in the changelog or the design page.

### 14. Documentation

- `docs/agents/deep-research-loop.md`: change the status line from "design, not implemented" to the state after this stage, and link the two stage pages.
- `docs/agent-defs.md`: document `loop` and the new knobs.
- `docs/deep-research-agent.md`: the loop section and the "Not included yet" list.
- `docs/research.md`: the run states and the stop reasons, if they are described there.
- `docs/listings/db-tables.md`: the two new run columns are not tables, so no change there unless a table was added in this stage.

## Tests

- Pure core: the turn builder, eviction, the gates, the breaker check, the phase transitions, each with tests that need no model and no DB. Include a property-style test that a random sequence of frontier and notes operations never breaks the transition invariant of stage 1.
- Loop with a fake model: scripted model replies (tool calls and text-only turns) drive a full exploration and synthesis on the SQLite store. Check: the reminder on a text-only turn, the forced extraction turn, the consolidation turn, the gate failure messages, the reopen path, a breaker in exploration (moves to synthesis, run completes), a breaker in synthesis (run fails with the breaker named), the forced last turn.
- Regression: `loop = "simple"` agents produce the same tool set, prompt and results as before (golden test on the prompt of one simple turn).
- Schema and migrations: the migration tests of item 11 on both backends, and the notes version restore of item 3 (a deleted notes file is rebuilt from the latest version before the next turn).
- Run `./run_tests.sh`.

## Done when

- an agent definition with `loop = "investigative"` runs exploration and synthesis to a `finalized` stop reason on the fake-model test, and on a real model for a small topic;
- the notes file of every investigative run is named with the sanitized `user_id`, the run id and its `notes_id`, and its versions are in `gb_agent_notes_versions`;
- every breaker in the design's table is tested, and the reserve is tested;
- `loop = "simple"` is unchanged (golden test), and it is still the default;
- the design page status and the docs listed above are updated;
- the migrations of item 11 are in place for both backends, and `db-tables.md` is current;
- `CHANGELOG.md` and `VERSION` are updated (minor bump: new loop, new knobs, new run columns);
- out-of-scope items and known gaps (for example the admin UI tree, the eval comparison numbers, parallel workers) are recorded in `leftover_tasks/` (see `gligabao/AGENTS.md`).
