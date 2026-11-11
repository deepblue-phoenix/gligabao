# Deep-research loop: frontier, notes and two-phase research (design)

**Status:** implemented in two stages: [stage 1](deep-research-loop-stage-1.md) (state, storage and tools, 0.180.0) and [stage 2](deep-research-loop-stage-2.md) (the loop, gates, breakers, knobs and observability, 0.181.0; `loop = "investigative"` in an agent definition, `simple` stays the default). This page is the specification the implementation follows. It defines the concepts, the data structures, the DB schema and the behaviour of the loop; it does not prescribe code layout beyond the constraints in [Constraints](#constraints). The agent as it works today is described in [deep-research-agent.md](../deep-research-agent.md); sub-agents in [subagents.md](subagents.md); the job around it in [research.md](research.md).

## Why

Today the research agent is a plain tool-calling loop whose conversation only grows, and whose stop condition is "the model wrote text instead of calling a tool". That gives four failure modes:

| Failure | Cause today | Answer in this design |
| ------- | ----------- | --------------------- |
| **Rabbit holes**: the agent follows tangents | no explicit list of what is still to be found out | the [question frontier](#the-question-frontier): a bounded, prioritized, depth-capped queue |
| **Premature termination**: one vague article, then a report | stopping is implicit | explicit `finalize_report` tool, gated by [exit criteria](#finalization-gate), and a separate [exploration/synthesis split](#two-loops) |
| **Context bloat**: every snippet stays in the history, cost grows per step | nothing is dropped | [externalized working state](#working-state-notesmd) and [context compaction](#context-compaction) |
| **Unchecked claims** | verification is only a line in the prompt | the [claim ledger](#claims-and-cross-checking) with `UNVERIFIED` -> `VERIFIED` rules |

The agent becomes an **investigator**: it keeps explicit state, decides what to find out next, acts, observes, and updates the state, testing hypotheses, triangulating sources and correcting itself.

## Concepts

- **Run**: one research (an existing `gb_agent_research_runs` row). Everything below is scoped to a run.
- **Objective**: the topic of the run; the root question of the frontier.
- **Question**: an unresolved query, hypothesis or knowledge gap; a node of the frontier graph.
- **Frontier**: the set of questions of a run with their edges (parent, dependencies). It is the active boundary between what is known and what must be found next.
- **Claim**: an atomic factual assertion with its sources and a verification status.
- **Datapoint**: a claim that is a number with a unit and context.
- **Notes** (`notes.md`): the text scratchpad of the run, the agent's working state.
- **Turn**: one model call plus the tools it asked for.
- **Iteration**: one pass of the [cycle](#the-cycle) on one question; usually several turns.
- **Circuit-breaker**: a deterministic limit that ends the loop regardless of what the model wants.

## Constraints

- **Functional style** (mandatory, see `AGENTS.md`): the frontier, the notes and the loop state are immutable values; every transition is a pure function `(state, event) -> state`; reads and writes to the DB, the notes file and the model are at the edge (an effect layer that executes what the pure core returned). Selection, pruning, gating and breaker checks are pure functions of the state and are unit-testable without a model or a DB.
- **Minimal**: no framework, no new service, no concept that the sections below do not need.
- **Deterministic where possible**: everything that decides *whether the loop continues* (selection order, dependency checks, depth cap, attempt cap, breakers, finalization gate) is code, not model judgement. The model decides *content*: what to ask, how to search, what an answer says, whether it meets the criteria.
- The frontier lives **outside the prompt and outside `notes.md`**, in its own tables. The model touches it only through tools.

## The cycle

Each iteration is state -> decide -> act -> observe -> update:

```
 ┌─────────────┐   ┌─────────────┐   ┌─────────────┐   ┌─────────────┐   ┌─────────────┐
 │ 1. STATE    │──▶│ 2. DECIDE   │──▶│ 3. ACT      │──▶│ 4. OBSERVE  │──▶│ 5. UPDATE   │──┐
 │ build prompt│   │ select the  │   │ search /    │   │ extract,    │   │ frontier,   │  │
 │ from notes +│   │ question,   │   │ fetch /     │   │ evaluate vs │   │ notes,      │  │
 │ frontier    │   │ plan query  │   │ query ...   │   │ criteria    │   │ claims      │  │
 └─────────────┘   └─────────────┘   └─────────────┘   └─────────────┘   └──────┬──────┘  │
        ▲                                                                       │         │
        │                    circuit-breaker check / finalize_report            ▼         │
        └───────────────────────────────────────────────────────────────────────┴─────────┘
```

1. **State.** The loop builds the prompt from: the task (objective), the tool definitions, the current `notes.md`, the frontier summary, and the last 1 to 2 turns of raw tool input/output (see [context compaction](#context-compaction)). Nothing else.
2. **Decide.** Code selects the question to work on (see [selection](#selection)). The model is told which question it is, with its rationale, validation criteria and already-attempted queries, and formulates the next action **to satisfy the validation criteria**, not generic browsing.
3. **Act.** The model calls tools: `web_search`, `fetch_page`, `query`, and the frontier and notes tools.
4. **Observe.** Results come back. A large `fetch_page` result triggers the [forced extraction](#forced-extraction) before anything else. The model evaluates what it has against the validation criteria of the question.
5. **Update.** The model records the outcome through tools (resolve, reformulate, spawn children, record claims); the code applies the deterministic parts (attempt counting, blocking, pruning, re-ranking), runs the breaker check and starts the next iteration.

The loop ends only by `finalize_report` or a circuit-breaker (below); a turn where the model answers with plain text and no tool is not an end: the loop reminds it of the open frontier and continues.

### Hypothesis testing, triangulation, self-correction

These are not extra machinery; they are uses of the frontier and the claim ledger:

- **Hypothesis testing.** A question may be a hypothesis (`kind = 'hypothesis'`: "X is true because Y"). Its validation criteria state what would confirm it and what would refute it. The agent searches for **both**: one query for confirming evidence and an adversarial one for refuting evidence (see [adversarial queries](#claims-and-cross-checking)). Result: `resolved` (confirmed or refuted: the claim ledger records which), or `unresolvable`.
- **Source triangulation.** A claim becomes `VERIFIED` only with support from at least `min_independent_sources` (default 2) **independent** sources (different registrable domain and not one quoting the other; the agent states the independence in the claim record). One source alone leaves it `UNVERIFIED`.
- **Self-correction.** Anything recorded can be revised: a claim can be `REFUTED` or `RETRACTED`, a resolved question can be `reopened` when a later finding contradicts its answer (the `reopen_question` tool; it re-enters the queue with raised priority and a note why), and a refuted claim spawns a high-priority question to resolve the contradiction. Revisions are appended, not overwritten, so the history stays auditable.

## The question frontier

An explicit, prioritized queue of unresolved questions, treated as a best-first search over a knowledge space: **every node is a question, every edge a retrieval step, the frontier stores the candidate nodes waiting to be evaluated.** It is the termination criterion too: the exploration phase is finished when no actionable question remains (or a breaker fires).

### Question record

| Field | Type | Meaning |
| ----- | ---- | ------- |
| `id` | text | stable id within the run, `q_001`, `q_002`, ... (root is `q_001`); assigned by code |
| `parent_id` | text, nullable | the question this one was spawned from; NULL for the root |
| `depth` | int | hops from the root (root is 0); `parent.depth + 1`, computed by code |
| `kind` | enum | `question` or `hypothesis` |
| `question` | text | the question or the hypothesis statement |
| `rationale` | text | why it matters for the objective (what it unblocks) |
| `priority` | real 0..1 | how critical the answer is to the objective and to the questions that depend on it |
| `status` | enum | see [lifecycle](#lifecycle) |
| `validation_criteria` | text | what counts as an acceptable answer; required before the question can be `resolved` |
| `target_sources` | text list | kinds of source to look for (`benchmarks`, `source_code`, `paper_tables`, `official_docs`, ...) |
| `attempts` | int | number of distinct query formulations tried (derived from the attempt log) |
| `answer` | text | the resolving answer (short) with claim ids; set on `resolved`; for `unresolvable`, the logged assumption |
| `created_at`, `updated_at` | time | |

Dependencies are edges (`question_id` depends on `depends_on_id`), kept in their own table so they can be validated: a question is **actionable** only when every dependency is `resolved` (a dependency that is `pruned` or `unresolvable` makes the dependant `blocked`; the agent must then rewrite or drop it). Dependencies must form a DAG; adding an edge that would create a cycle is rejected.

Each query tried on a question is a row of the **attempt log** (`question_id`, the query text, tool, outcome summary, time). It is what the agent sees as "already attempted" and what drives mutation and the dead-end rule.

Example (as the tools return it):

```json
{
  "id": "q_042",
  "parent_id": "q_001",
  "depth": 2,
  "kind": "question",
  "question": "What is the memory overhead of the U-Net skip-connections during 2D spatial indexing?",
  "rationale": "Required to verify whether the architecture fits within a 16GB VRAM budget.",
  "priority": 0.85,
  "status": "pending",
  "dependencies": ["q_012"],
  "validation_criteria": "Concrete byte or tensor allocation figures from benchmarks or profiling logs.",
  "retrieval_strategy": {
    "target_sources": ["benchmarks", "source_code", "paper_tables"],
    "attempted_queries": ["residual unet skip connection memory consumption fp16"]
  }
}
```

(`retrieval_strategy` is a view: `target_sources` is a column, `attempted_queries` comes from the attempt log.)

### Lifecycle

```
            ┌──────────── reopen_question ────────────┐
            ▼                                         │
 pending ──select──▶ in_progress ──criteria met──▶ resolved
    │                    │
    │                    ├──inconclusive, attempts < max_attempts──▶ pending (mutated query, attempts+1)
    │                    └──attempts == max_attempts──▶ unresolvable   (assumption logged, gap goes in the report)
    ├──dependency pruned/unresolvable──▶ blocked
    └──branch invalidated / does not alter the conclusion / depth cap──▶ pruned
```

- `pending`: waiting in the queue.
- `in_progress`: selected; at most `max_parallel` at a time (1 for a single worker).
- `resolved`: validation criteria met; the answer and its claim ids are stored.
- `blocked`: a dependency can never be satisfied; needs a rewrite by the agent, or it is pruned.
- `unresolvable`: failed after `max_attempts` distinct reformulations. It is **not retried**; the gap and the assumption made go into the report.
- `pruned`: dropped from the search (with a reason). Terminal, but kept in the table for the audit trail.

Terminal states: `resolved` (can be reopened), `unresolvable`, `pruned`.

**Every status change is recorded as a new row** in `gb_agent_frontier_transitions` (append-only, one row per change, never an update of an old row), referencing the question by `(run_id, question_id)`. The row holds the old and new status, what triggered it, whether the agent or a deterministic rule did it, the reason, the priority and attempt count at that moment, and what caused it (another question or a challenge). The same transaction updates the current `status` of the question, so a question is a row (current value) plus its transitions (history). Creation is the first transition (`created`, no old status); a selection, a retry that returns the question to `pending`, a reopen, an automatic block or unblock, and every prune (including each descendant pruned by `prune_branch`, one row each) are all transitions. A change of priority alone is not a status change: it is a transition with `from_status = to_status` and trigger `reprioritized`, so priority history is on the same timeline.

### Selection

Pure function over the frontier: among `pending` questions whose dependencies are all `resolved`, pick the highest `priority`; ties go to the lower `depth` (broader first), then to the older one. If the highest-priority `pending` question is not actionable because of an unresolved dependency, the dependency is considered first (its own priority is raised to at least that of its dependant, so unblocking work is never starved). Selecting sets `in_progress`.

### Evaluation and mutation

After acting, the model judges the observations against the question's `validation_criteria` and calls one of:

- `resolve_question(id, answer, claim_ids)`: the code checks that claim ids exist, that a `hypothesis` carries both a confirming and a refuting search in its attempt log (or an explicit reason), and that each cited claim is at least `UNVERIFIED`; it then sets `resolved`.
- `retry_question(id, new_query, why)`: inconclusive; logs the attempt (the code rejects a query that is not different from earlier ones) and returns the question to `pending`. When `attempts` reaches `max_attempts` (default 4) the code sets `unresolvable` instead and requires an `assumption` text.

### Branching (node expansion)

`add_question(...)` creates a child of the current question when a document brings:

- a **contradiction** with something recorded,
- an **unexpected dependency** (a prerequisite fact the agent did not know it needed),
- a knowledge gap that blocks the validation criteria.

The code assigns `id` and `depth`, rejects the node when `depth > max_depth` (default 3; returned to the agent as "pruned by depth cap; record it as an open gap in notes instead"), rejects a near-duplicate of an existing question, and enforces `max_questions` per run. A child that unblocks the current question is added as a dependency of it and gets priority at least as high as the current question: **an anomaly is resolved before the agent moves forward.**

### Pruning and re-prioritization

After every update the loop runs pure cleanups, and the agent can request more:

- **Scope pruning.** `prune_branch(id, reason)`: drops the question and all `pending`/`blocked` descendants (recursively). Used when an answer makes a branch irrelevant (a library found deprecated removes all questions about configuring it). The prompt tells the agent to prune any question whose answer **would not alter the top-level conclusion**; the reason is stored.
- **Depth cap.** Nodes deeper than `max_depth` are never created.
- **Dead ends.** `unresolvable` after `max_attempts`, never spun on.
- **Re-prioritization.** `set_priority(id, priority, why)`. The code also re-ranks mechanically: raising a question raises the floor of its dependencies; resolving a question lowers nothing but unblocks dependants; a pruned branch removes its weight from ancestors.

### Frontier tools

All mutations go through tools; each returns the updated compact summary of the affected questions. The full frontier is never dumped into the prompt; only the [summary](#prompt-contents).

| Tool | Purpose |
| ---- | ------- |
| `frontier_next()` | select (pure function above) and return the next question, marking it `in_progress` |
| `frontier_view(status?, id?)` | inspect questions (compact), or one question with its attempt log |
| `add_question(parent_id, question, rationale, priority, validation_criteria, target_sources, depends_on?, kind?)` | expand the graph |
| `retry_question(id, new_query, why)` | record an inconclusive attempt, mutate the query |
| `resolve_question(id, answer, claim_ids)` | mark resolved |
| `reopen_question(id, why, priority?)` | self-correction |
| `set_priority(id, priority, why)` | re-prioritize |
| `prune_branch(id, reason)` | scope pruning |

At the start of a run the agent performs **broad query expansion**: from the objective it creates the first level of sub-questions (typically 3 to 8, each with criteria and priority) before the first search. The first `frontier_next` is rejected until the root has at least one child (or the agent states that the objective is atomic).

## Working state: `notes.md`

The agent's externalized working memory for the run: a text file the agent reads (it is always in the prompt) and writes through tools. It holds **knowledge and reasoning**, while the frontier (control flow) is in the DB.

Storage has two parts, both per run:

- **Working copy: a file.** Its name is `notes_<user>_<run>_<notes_id>.md`, in the notes directory (server config `research_notes_dir`, default in the research data directory next to the other research files).
  - `<user>` is the run's `user_id` with every character outside `A-Z a-z 0-9 . _ -` replaced by `_`, cut to 64 characters. A `user_id` is any string (see [identity.md](../identity.md)), so it must be sanitized. It is in the name for readability only: uniqueness comes from `<run>` and `<notes_id>`.
  - `<run>` is `gb_agent_research_runs.id`. `<notes_id>` is a random 128-bit id (32 hex characters) made when the run is created and kept in the run's `notes_id` column. Two servers sharing a directory, or a database whose ids were reused, cannot overwrite each other's files.
  - The file is written to a temp file in the same directory and renamed over the old one.
- **History: the database.** Every write also adds a full copy of the file to `gb_agent_notes_versions` (see [schema](#database-schema)). The file is the copy the loop reads and edits; the versions are the record for debugging and export. If the file is missing when a turn starts, the loop restores it from the latest version.

The storage is behind a small read/write interface, so that it can move (for example to an object store) without touching the loop. The sections are fixed so the loop can render, size-check and gate on them:

```
# Notes: <objective>

## Verified           (claims with status VERIFIED)
- [c_007] VERIFIED  The model needs 11.2 GB at fp16.  src: s_3, s_5
## Unverified         (claims awaiting cross-check; each lists what would verify it)
- [c_009] UNVERIFIED  Skip connections add ~18% memory.  src: s_4  | check: independent benchmark
## Contradictions     (conflicts between claims, with the question spawned to resolve them)
- c_007 vs c_011 -> q_044
## Datapoints         (numbers with unit and context)
- [d_002] 16 GB | VRAM budget of the target GPU | s_1
## Sources            (every source used: id, URL, title, access note, domain)
- [s_3] https://... "Title" (arxiv.org)
## Assumptions & gaps (unresolvable questions and the assumptions made)
- q_031 unresolvable: no public figure found; assumed ~2x of baseline
## Scratch            (free text: reasoning, plans; bounded size)
```

Rules:

- Ids (`c_`, `d_`, `s_`) are assigned by code when the agent appends; the agent refers to them in the frontier tools and in the report.
- One record per line, in a fixed grammar, so that the gate and the compaction can read them with a pure parser. Anything in `Scratch` is free text and not parsed.
- Edits go through tools: `notes_append(section, entries)`, `notes_update(id, change)` (change status, add a source, retract), `notes_scratch(text)`. The agent does not rewrite the file wholesale, so a model slip cannot erase verified work.
- **Size bound:** the file has a budget in tokens (`notes_max_tokens`, default 6000). When it is over, the loop forces a **consolidation turn** (merge duplicates, shorten `Scratch`, move the settled to `Verified`) before continuing. This keeps the prompt bounded.
- Every write adds a version row to `gb_agent_notes_versions`, in the same transaction as the claim/source/transition rows it changes, so the versions and the structured history always agree.

## Extraction

### Forced extraction

`fetch_page` (the renamed, richer successor of `fetch_url`) returns the page text. When it returns more than `extraction_threshold` characters (default 4,000), the loop does **not** let the agent continue with the raw text sitting in the conversation:

1. The next turn is restricted: only the notes tools are available, and the instruction is "extract what matters for question `q_xxx` from this page".
2. The agent records **atomic claims** (one assertion each, self-contained), **numeric datapoints** (value, unit, context) and the **source link** (with title) in `notes.md` (claims start `UNVERIFIED`, linked to the source id).
3. When the turn ends, the raw page text becomes eligible for eviction (see below). A page for which nothing was extracted is recorded as a source with a line "read, nothing relevant" so it is not fetched again.

The same rule applies to any tool result larger than the threshold (a `query` answer, a long search briefing).

### Claims and cross-checking

A claim has: `id`, `text`, `status`, source ids, `supports` / `contradicts` links to other claims, and the question that produced it.

| Status | Meaning |
| ------ | ------- |
| `UNVERIFIED` | recorded from a primary source, not cross-checked |
| `VERIFIED` | supported by at least `min_independent_sources` independent sources |
| `REFUTED` | credible independent evidence contradicts it |
| `RETRACTED` | the agent withdrew it (source found unreliable or misread) |

Procedure:

1. On finding a primary claim, the agent records it `UNVERIFIED`.
2. For each `UNVERIFIED` claim that matters to a question, the loop makes the agent **challenge** it with an adversarial query (see [Adversarial queries](#adversarial-queries)). Every challenge is recorded in its own table.
3. Supporting evidence from a source that is independent of those already listed moves the claim to `VERIFIED` (`notes_update`); the code checks the source count and independence declaration, the model cannot mark `VERIFIED` with fewer.
4. Contradicting evidence sets a `contradicts` link, moves the claim to `REFUTED` or leaves both claims in `Contradictions`, and **spawns a high-priority question** (branching above).

Only `VERIFIED` claims (and explicitly flagged `UNVERIFIED` ones, marked as such) can appear in the report.

### Adversarial queries

An adversarial query is a search whose purpose is to **find evidence against** something the agent currently believes. The point is to test, not to look for more of the same: a query that would return the same pages as the one that produced the claim is not a challenge. Every challenge is a row in `gb_agent_challenges`, so that how the model attacks its own facts and conclusions can be inspected afterwards (what it targeted, how it phrased the attack, what came back, and whether the belief survived).

**What can be challenged (`target_type`):**

- a `claim` (an atomic fact or datapoint): challenged when it is `UNVERIFIED` and matters to a question's criteria, and again before it is allowed to count as `VERIFIED` if it was verified only by sources with a common origin;
- a `question_answer`: the answer of a question being `resolved`, challenged before `resolve_question` is accepted for a question of depth 0 or 1 (the ones that carry the conclusion);
- a `conclusion`: the overall conclusion of the run, challenged once at the end of exploration (the conclusion is stated in a few sentences in `notes.md`, section `Scratch`, and referenced by the challenge). This is what keeps the agent from stopping on the first plausible story.

**How a challenge is formed.** The model does not write the query directly: it states first, in structured fields, what it is attacking and by which **strategy**, then the query. Code validates the fields and rejects a challenge that is not adversarial (see below). The fields:

| Field | Content |
| ----- | ------- |
| `negation` | the belief inverted into the statement to be proven (claim "X needs 11 GB" -> "X needs substantially more or less than 11 GB") |
| `strategy` | one of the strategies below |
| `query` | the search text, built from the `negation` and the strategy, not from the original wording |
| `expected_if_false` | what the evidence would look like if the belief were wrong (so the result can be judged without bias) |

Strategies (`strategy`), the vocabulary the model chooses from, so challenges can be counted and compared:

| Strategy | Query aims at | Example (claim: "Library L supports feature F") |
| -------- | ------------- | ----------------------------------------------- |
| `negation` | the opposite statement | "L does not support F" |
| `discrepancy` | a different figure or date for the same quantity | "L feature F support version conflicting documentation" |
| `criticism` | known problems, retractions, errata, criticism | "L feature F broken issue" |
| `alternative_method` | a different measurement, dataset or methodology that could give another result | "F benchmark L independent reproduction" |
| `scope_limit` | conditions under which the claim fails (version, hardware, population, time) | "L F only works when ..." |
| `primary_source` | the original source behind a secondary claim, to find out whether it says so | "L release notes F" |
| `staleness` | newer information that supersedes the claim | "L F deprecated 2025" |

Rules the code enforces (pure checks on the challenge value):

1. A challenge names its target (`claim_id`, `question_id` or the conclusion) and a `strategy`; it carries at least one `expected_if_false`.
2. The `query` differs from every earlier query on the question (and from the queries of earlier challenges of the same target), normalized; a rephrasing is rejected.
3. The same `strategy` is not used twice on the same target; at most `max_challenges_per_target` (default 3) on one target.
4. The query must not be only the target's own text: it must contain terms of the `negation`/strategy that the original claim text does not (a cheap string test, so "claim + the word 'not'" is allowed and "claim again" is not).
5. It is executed as a normal `web_search`/`fetch_page`, and is also written to the attempt log of the question (the challenge row points to that attempt).

**Recording the outcome.** After the observation (and the forced extraction, if any) the agent calls `record_challenge_result` with a verdict:

| `outcome` | Meaning | Effect |
| --------- | ------- | ------ |
| `survived` | adversarial search found nothing credible against the target | counts toward verification; the claim may move to `VERIFIED` (with the source rule) |
| `weakened` | found qualifications, narrower scope or a lower-quality counter-source | the target is amended (scope added to the claim text) and stays `UNVERIFIED` or is re-verified |
| `refuted` | credible independent evidence contradicts the target | claim -> `REFUTED` (`contradicts` link), a high-priority question is spawned, the answer or conclusion is reopened (`reopen_question`) |
| `inconclusive` | nothing usable either way (poor results, paywalls) | another challenge with another strategy is allowed (up to the cap); never counts as `survived` |

The verdict comes with the evidence: `evidence` (a short text, what was found), and the source ids. The code requires `refuted` and `weakened` to cite at least one source, so the model cannot declare a refutation from nothing; and it limits `survived` to a challenge that was executed (has an attempt). `result_claim_id` links the new claim that the counter-evidence produced, if any.

**When the loop requires challenges.**

- A claim cannot become `VERIFIED` without at least `min_challenges` (default 1) challenges with outcome `survived` on it, on top of the source count.
- `resolve_question` for depth 0 and 1 questions requires a challenge of the answer (`question_answer`), or a stored reason.
- `finish_exploration` requires a challenge of the `conclusion` with a recorded outcome (see the [gate](#finalization-gate)); if it is `weakened` or `refuted` the exploration continues on the questions this opened.
- After a `refuted` outcome on a claim, every question that cited that claim in its `answer` is reopened by code.

**Tools:**

| Tool | Purpose |
| ---- | ------- |
| `challenge(target_type, target_id?, negation, strategy, expected_if_false, query)` | validate and register a challenge; returns its id and runs nothing itself (the agent then calls `web_search` or `fetch_page` with the registered query) |
| `record_challenge_result(challenge_id, outcome, evidence, source_ids, result_claim_id?)` | close the challenge with its verdict |
| `challenges_view(target_id?, status?)` | list the challenges (compact), e.g. open ones or those on a target |

A challenge is `open` between `challenge` and `record_challenge_result`. Open challenges appear in the frontier summary of the prompt, so the agent cannot forget to close one; the loop will not start `finish_exploration` with one open.

**What it enables for analysis.** Because the strategy, the negation, the query, the outcome and the evidence are structured, one can answer: how often does the agent challenge at all; which strategies it favours and which find the refutations; how many claims survive; whether the challenges are real (are queries distinct, do they use new terms) or token (all `survived`, all with the same strategy); which sources refuted what; and how the model's conclusion changed. See the [observability section](#observability).

## Context compaction

The active message history is **rebuilt each turn**, not accumulated. The prompt of a turn contains only:

1. the system prompt and the **tool definitions**,
2. the **task** (the objective, as the first user message),
3. the **scratchpad**: the current `notes.md` and the frontier summary (see below),
4. the **last 1 to 2 turns** of raw tool input/output (`keep_turns`, default 2).

Everything older is evicted. A tool output may be evicted only when it has been **committed**: its useful content is in `notes.md`, or the loop has established that nothing in it is useful (recorded in the attempt log with an outcome summary). The attempt log keeps the queries and a one-line outcome, so the agent still knows what it tried without the snippets. An evicted result is replaced by nothing in the history (the scratchpad is the memory), not by a stub, apart from the attempt-log line.

Consequence: the cost of a turn is bounded by `task + tools + notes (<= notes_max_tokens) + frontier summary + 2 turns`, not by the number of turns so far, so the run can have many more iterations than today's 20 steps.

Uncommitted results (the agent has not extracted yet) are never evicted: the forced-extraction step guarantees a commit point before the window moves on.

### Prompt contents

Frontier summary rendered into the prompt (compact, bounded):

```
Frontier: 7 resolved, 5 pending, 1 unresolvable, 3 pruned  (max_depth 3)
Current: [q_042] (P 0.85, depth 2) <question>
  criteria: <validation_criteria>
  tried: "<query 1>", "<query 2>"
Next up:
- [q_043] (P 0.80) ...
- [q_050] (P 0.55) ...
Blocked: [q_051] waits on q_042
```

## Two loops

To prevent premature termination, a research is two loops run one after the other, with different tools, prompts and goals.

### Exploration loop

Goal: gather and validate evidence until the frontier is exhausted. Tools: web/knowledge tools, frontier tools, notes tools, `finish_exploration`. The model cannot write the report here. It ends when:

- `finish_exploration` is called and the [exploration gate](#finalization-gate) passes, or
- the frontier has no actionable question left (the loop then does a final check: any `UNVERIFIED` claim relevant to a resolved question is queued for verification; if there is none, exploration ends), or
- a [circuit-breaker](#circuit-breakers) fires.

### Synthesis loop

Goal: write the report from the notes and the frontier, with **no new research**. Input: the objective, `notes.md` (verified claims, datapoints, sources, contradictions, assumptions and gaps) and the frontier summary including `unresolvable` and `pruned` questions with reasons. Tools: `notes_read`, `frontier_view`, and `finalize_report(report)`. It may not search; if it finds a hole it cannot fill it must state it in the report (a hole that would change the conclusion may reopen exploration once: `finalize_report` can return to exploration with a reopened question, at most `max_reopens` times, default 1).

The report has the same shape as today (summary first, then details, then the source list) plus a section on **gaps and assumptions** taken from `unresolvable` questions, and the verification status of key claims. Every statement is traceable to claim and source ids, rendered as URLs.

**`finalize_report`** is the completion tool. It is the only normal way for a run to complete. It is called only in the synthesis loop; the text passed is the report stored in `gb_agent_research_runs.report`.

### Finalization gate

Deterministic checks in code, returned to the agent as an error listing what is missing when they fail:

Exploration gate (`finish_exploration`):

- the root question is `resolved`, or `unresolvable` with a stated assumption,
- no `pending` question with `priority >= gate_priority` (default 0.5) that is actionable,
- at least `min_sources` (default 5, scaled down by the run options for narrow tasks) distinct sources in notes, from at least `min_domains` (default 3) different domains,
- every `resolved` question cites at least one `VERIFIED` claim, or explains (stored) why `UNVERIFIED` is the best available,
- no unaddressed entry in `Contradictions`,
- no `open` challenge, and the `conclusion` has a closed challenge whose outcome is `survived` (after `weakened` or `refuted` the exploration goes on).

Finalization (`finalize_report`): the report cites only existing source ids; every `unresolvable` question appears under gaps; nothing in the report contradicts a `REFUTED` claim (checked by claim-id references, not by text analysis).

## Circuit-breakers

Checked by code **before every model call**; none depends on the model's cooperation.

| Breaker | Default | Effect |
| ------- | ------- | ------ |
| `max_iterations` | 60 turns for exploration (run option; replaces the `max_steps` of the loop; today's 1..40 limit is raised) | forces the phase to end |
| `max_depth` | 3 | not a loop stop: questions deeper are never created |
| `max_attempts` per question | 4 | question becomes `unresolvable` |
| `max_questions` per run | 40 | no more nodes; agent must prune or resolve |
| token ceiling | `max_total_tokens` (input + output of the run) | forces the phase to end |
| cost budget | `max_cost_usd` (the existing [cost cap](agent-defs.md#limits-cost-caps)) | forces the phase to end |
| wall clock | `timeout` (existing) | the run ends as today |

When a breaker fires during exploration, the loop **does not fail the run**: it moves directly to synthesis, with a note in the notes file and in the report that the investigation was cut short and by what (`breaker: cost`), listing the still-pending questions as open gaps. Synthesis has its own reserved budget (`synthesis_reserve`, default 15% of the iteration/token/cost limits) that exploration cannot spend, so a report can always be written. If even the reserve is exhausted, the run ends as `failed` with the breaker named in `error` (today's behaviour for an agent that ends without a report). The last turn is always forced to be `finalize_report` (`tool_choice` on that tool), replacing today's "last step has no tools".

## Parallelism and sub-agents

The frontier is the unit of work distribution. With one worker the loop selects one question at a time. The design allows more: several workers (the existing [sub-agents](subagents.md)) can each take an `in_progress` question; selection and status changes are atomic in the DB (a question is claimed with a conditional update, `pending -> in_progress` with the worker's run id), and `max_parallel` bounds them. The sub-agents of a run share its frontier, notes and claim ledger; `spawn_subagent` is called with a `question_id` so the sub-agent's task is that node, its report resolves it. How sub-agents write to the shared notes (serialized appends through the same tools) is an implementation matter; the first implementation may run with `max_parallel = 1` and leave this off.

## Database schema

New tables, in `internal/agent_research/db/{sqlite,postgres}/schema.sql` (final state, idempotent, same tables in both, columns documented in the SQLite file) and listed in `docs/listings/db-tables.md`. All are scoped to a run and deleted with it (`ON DELETE CASCADE`). The frontier is **not stored in `notes.md`**. The notes file is a working copy on disk; its history is `gb_agent_notes_versions`. The claim/source tables below are the structured history behind the notes sections.

**Migrations.** Every startup applies the schema to the database the server opens (the `Open` of the package, `internal/agent_research/ops.go`):

- A **new table** is a `CREATE TABLE IF NOT EXISTS` in `schema.sql`. It is created on start and needs nothing else.
- A **change to an existing table** (a new column, a new index) is an `ALTER` in the package's upgrade list, run on every start. `CREATE TABLE IF NOT EXISTS` never changes a table that already exists, so the `schema.sql` file alone is not enough for an older database. SQLite has no `ADD COLUMN IF NOT EXISTS`: each `ALTER ... ADD COLUMN` runs only when `pragma_table_info` does not list the column. PostgreSQL uses `ADD COLUMN IF NOT EXISTS`. The same column also goes into `schema.sql`, so a new database gets it directly.
- Indexes on an added column are created after the upgrade (as `schema_subagent_indexes.sql` is).
- A column added by `ALTER` may have a simple `CHECK`. Changing an existing `CHECK` needs a table rebuild in SQLite (the `migrateSQLiteStatus` pattern), so avoid it.

Frontier:

```sql
-- One row per question of a run (a node of the frontier graph). ids are per-run ('q_001' is the root).
CREATE TABLE IF NOT EXISTS gb_agent_frontier_questions (
	run_id              INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id                  TEXT NOT NULL,
	parent_id           TEXT,
	depth               INTEGER NOT NULL CHECK (depth >= 0),
	kind                TEXT NOT NULL DEFAULT 'question' CHECK (kind IN ('question', 'hypothesis')),
	question            TEXT NOT NULL CHECK (question <> ''),
	rationale           TEXT NOT NULL DEFAULT '',
	priority            REAL NOT NULL CHECK (priority >= 0 AND priority <= 1),
	status              TEXT NOT NULL DEFAULT 'pending'
	                    CHECK (status IN ('pending', 'in_progress', 'resolved', 'blocked', 'unresolvable', 'pruned')),
	validation_criteria TEXT NOT NULL DEFAULT '',
	target_sources      TEXT NOT NULL DEFAULT '[]',   -- JSON array of strings
	answer              TEXT NOT NULL DEFAULT '',     -- resolved: the answer; unresolvable: the assumption; pruned: the reason
	claimed_by          INTEGER REFERENCES gb_agent_research_runs(id) ON DELETE SET NULL, -- worker run while in_progress
	created_at          DATETIME NOT NULL,
	updated_at          DATETIME NOT NULL,
	PRIMARY KEY (run_id, id),
	FOREIGN KEY (run_id, parent_id) REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_agent_frontier_questions_pick
	ON gb_agent_frontier_questions(run_id, status, priority DESC, depth);

-- Edges "question depends on depends_on_id" (a DAG; cycles are rejected by the code before insert).
CREATE TABLE IF NOT EXISTS gb_agent_frontier_deps (
	run_id        INTEGER NOT NULL,
	question_id   TEXT NOT NULL,
	depends_on_id TEXT NOT NULL,
	PRIMARY KEY (run_id, question_id, depends_on_id),
	CHECK (question_id <> depends_on_id),
	FOREIGN KEY (run_id, question_id)   REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE,
	FOREIGN KEY (run_id, depends_on_id) REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE
);

-- APPEND-ONLY attempt log: one row per query tried on a question (drives "attempted_queries" and the dead-end rule).
CREATE TABLE IF NOT EXISTS gb_agent_frontier_attempts (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,   -- BIGSERIAL in PostgreSQL
	run_id      INTEGER NOT NULL,
	question_id TEXT NOT NULL,
	seq         INTEGER NOT NULL CHECK (seq >= 1),   -- 1, 2, ... per question
	query       TEXT NOT NULL CHECK (query <> ''),
	tool        TEXT NOT NULL DEFAULT '',            -- web_search, fetch_page, query (a challenge row points to its attempt)
	outcome     TEXT NOT NULL DEFAULT '',            -- one-line summary of what it yielded
	created_at  DATETIME NOT NULL,
	UNIQUE (run_id, question_id, seq),
	FOREIGN KEY (run_id, question_id) REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE
);

-- APPEND-ONLY: ONE ROW PER STATUS CHANGE OF A QUESTION (never updated, never deleted but with the run). The current status stays in
-- gb_agent_frontier_questions.status; the code writes that column and the transition row in the SAME transaction, so the two never disagree
-- and the latest row of a question always equals its status. Replaying the rows of a run rebuilds the history of the whole search.
--   question_id    the question (gb_agent_frontier_questions.id, same run)
--   seq            1, 2, 3, ... per question (unique with the question): the order of its changes; seq 1 is the creation (from_status NULL)
--   from_status    the status before ('' on creation)
--   to_status      the status after: pending, in_progress, resolved, blocked, unresolvable or pruned
--   trigger        what caused it, a closed vocabulary: created, selected, resolved, retried, attempts_exhausted, reopened, dependency_failed,
--                  dependency_met, reprioritized (from_status = to_status), pruned_branch, pruned_depth, pruned_scope, contradiction
--   actor          'agent' (a tool call of the model) or 'code' (a deterministic rule: attempt cap, branch pruning, unblocking)
--   reason         the agent's stated reason, or the rule that fired (for resolved: the answer's gist; for unresolvable: the assumption)
--   priority       the question's priority at that moment (so re-prioritization is visible between rows)
--   attempts       attempts made so far
--   caused_by      the question or challenge that triggered it, if any ('q_044', 'ch_003'): e.g. a contradiction spawned q_044, which reopened q_012
--   state_seq      the research state (gb_agent_research_states.seq of the run) in which it happened, to tie it to the step/turn
--   created_at     when it happened
CREATE TABLE IF NOT EXISTS gb_agent_frontier_transitions (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,   -- BIGSERIAL in PostgreSQL
	run_id      INTEGER NOT NULL,
	question_id TEXT NOT NULL,
	seq         INTEGER NOT NULL CHECK (seq >= 1),
	from_status TEXT NOT NULL DEFAULT '',
	to_status   TEXT NOT NULL CHECK (to_status IN ('pending', 'in_progress', 'resolved', 'blocked', 'unresolvable', 'pruned')),
	trigger     TEXT NOT NULL CHECK (trigger <> ''),
	actor       TEXT NOT NULL CHECK (actor IN ('agent', 'code')),
	reason      TEXT NOT NULL DEFAULT '',
	priority    REAL NOT NULL CHECK (priority >= 0 AND priority <= 1),
	attempts    INTEGER NOT NULL DEFAULT 0,
	caused_by   TEXT NOT NULL DEFAULT '',
	state_seq   INTEGER NOT NULL DEFAULT 0,
	created_at  DATETIME NOT NULL,
	UNIQUE (run_id, question_id, seq),
	FOREIGN KEY (run_id, question_id) REFERENCES gb_agent_frontier_questions(run_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_gb_agent_frontier_transitions_run ON gb_agent_frontier_transitions(run_id, id);
```

Claims and sources (the structured record behind the claim lines of `notes.md`; the file is the rendered view, these tables let the gates and the exports work without parsing text):

```sql
-- Sources read in a run (one row per distinct URL).
CREATE TABLE IF NOT EXISTS gb_agent_sources (
	run_id     INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id         TEXT NOT NULL,                        -- 's_001'
	url        TEXT NOT NULL,
	domain     TEXT NOT NULL,                        -- registrable domain, for the independence check
	title      TEXT NOT NULL DEFAULT '',
	note       TEXT NOT NULL DEFAULT '',             -- e.g. 'read, nothing relevant'
	created_at DATETIME NOT NULL,
	PRIMARY KEY (run_id, id),
	UNIQUE (run_id, url)
);

-- Claims and datapoints. kind 'datapoint' fills value/unit.
CREATE TABLE IF NOT EXISTS gb_agent_claims (
	run_id      INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id          TEXT NOT NULL,                       -- 'c_001', 'd_001'
	question_id TEXT,                                -- the question that produced it
	kind        TEXT NOT NULL DEFAULT 'claim' CHECK (kind IN ('claim', 'datapoint')),
	text        TEXT NOT NULL CHECK (text <> ''),
	value       TEXT NOT NULL DEFAULT '',
	unit        TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'UNVERIFIED' CHECK (status IN ('UNVERIFIED', 'VERIFIED', 'REFUTED', 'RETRACTED')),
	created_at  DATETIME NOT NULL,
	updated_at  DATETIME NOT NULL,
	PRIMARY KEY (run_id, id)
);

-- Claim <-> source links; stance says whether the source supports or contradicts the claim.
CREATE TABLE IF NOT EXISTS gb_agent_claim_sources (
	run_id    INTEGER NOT NULL,
	claim_id  TEXT NOT NULL,
	source_id TEXT NOT NULL,
	stance    TEXT NOT NULL CHECK (stance IN ('supports', 'contradicts')),
	PRIMARY KEY (run_id, claim_id, source_id),
	FOREIGN KEY (run_id, claim_id)  REFERENCES gb_agent_claims(run_id, id) ON DELETE CASCADE,
	FOREIGN KEY (run_id, source_id) REFERENCES gb_agent_sources(run_id, id) ON DELETE CASCADE
);
```

Challenges (the adversarial queries, see [Adversarial queries](#adversarial-queries)):

```sql
-- ONE ROW PER ADVERSARIAL QUERY: how the agent attacks its own claims, answers and conclusion. Created by the 'challenge' tool,
-- closed by 'record_challenge_result'. Rows are never deleted but with the run; the verdict fields are set once, on closing.
--   target_type  'claim', 'question_answer' or 'conclusion'
--   target_id    the claim id (claim) or the question id (question_answer); '' for the conclusion
--   negation     the belief inverted into the statement the search tries to prove
--   strategy     negation, discrepancy, criticism, alternative_method, scope_limit, primary_source or staleness
--   expected_if_false  what the evidence would look like if the belief were wrong (written BEFORE the search)
--   query        the search text that was run
--   attempt_id   the row of gb_agent_frontier_attempts that executed it (NULL until it runs)
--   status       'open' until a verdict is recorded, then 'closed'
--   outcome      survived, weakened, refuted or inconclusive ('' while open)
--   evidence     what was found, short; required for weakened and refuted
--   result_claim_id  the claim the counter-evidence produced, if any
CREATE TABLE IF NOT EXISTS gb_agent_challenges (
	run_id            INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	id                TEXT NOT NULL,                  -- 'ch_001', per run
	question_id       TEXT,                           -- the question being worked on when it was raised
	target_type       TEXT NOT NULL CHECK (target_type IN ('claim', 'question_answer', 'conclusion')),
	target_id         TEXT NOT NULL DEFAULT '',
	negation          TEXT NOT NULL CHECK (negation <> ''),
	strategy          TEXT NOT NULL CHECK (strategy IN ('negation', 'discrepancy', 'criticism', 'alternative_method', 'scope_limit', 'primary_source', 'staleness')),
	expected_if_false TEXT NOT NULL CHECK (expected_if_false <> ''),
	query             TEXT NOT NULL CHECK (query <> ''),
	attempt_id        INTEGER REFERENCES gb_agent_frontier_attempts(id) ON DELETE SET NULL,
	status            TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
	outcome           TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'survived', 'weakened', 'refuted', 'inconclusive')),
	evidence          TEXT NOT NULL DEFAULT '',
	result_claim_id   TEXT,
	created_at        DATETIME NOT NULL,
	closed_at         DATETIME,
	PRIMARY KEY (run_id, id),
	CHECK ((status = 'open' AND outcome = '') OR (status = 'closed' AND outcome <> ''))
);
CREATE INDEX IF NOT EXISTS idx_gb_agent_challenges_target ON gb_agent_challenges(run_id, target_type, target_id);

-- Sources that backed a verdict (a challenge can cite several).
CREATE TABLE IF NOT EXISTS gb_agent_challenge_sources (
	run_id       INTEGER NOT NULL,
	challenge_id TEXT NOT NULL,
	source_id    TEXT NOT NULL,
	PRIMARY KEY (run_id, challenge_id, source_id),
	FOREIGN KEY (run_id, challenge_id) REFERENCES gb_agent_challenges(run_id, id) ON DELETE CASCADE,
	FOREIGN KEY (run_id, source_id)    REFERENCES gb_agent_sources(run_id, id) ON DELETE CASCADE
);
```

Notes on the schema:

- `gb_agent_challenges` is created after `gb_agent_frontier_attempts`, and its `id` ('ch_001') is assigned like the others. `outcome = 'refuted'` and `'weakened'` require rows in `gb_agent_challenge_sources` (checked by the code that closes the challenge). In PostgreSQL `attempt_id` is `BIGINT`.
- `VERIFIED` is only reachable when `gb_agent_claim_sources` has at least `min_independent_sources` `supports` rows with distinct `domain`s and at least `min_challenges` closed challenges with outcome `survived` on the claim (checked by the code that updates the status, not by a DB constraint).
- Question ids and claim ids are assigned by the code from the per-run counters (max existing id + 1) in the same transaction as the insert.
- The existing `gb_agent_research_states` gets new `state` values (not constrained): `planning` (broad expansion), `selecting`, `extracting`, `verifying`, `challenging`, `consolidating`, `synthesizing`.
- `gb_agent_research_runs` gets three columns, all by `ALTER` (see Migrations): `notes_id` (the random id of the notes file, `''` for runs made before), `phase` (`exploration`, `synthesis`) and `stop_reason` (`finalized`, `frontier_exhausted`, `breaker:iterations`, `breaker:tokens`, `breaker:cost`, `''` while running).
- `notes.md` (the working copy) is a file per run, named as in [Working state](#working-state-notesmd). Its history is `gb_agent_notes_versions`:

```sql
-- APPEND-ONLY: the whole notes.md after each write, for debugging and export (the file is the working copy; this is the record).
--   seq        1, 2, 3, ... per run, in write order
--   state_seq  gb_agent_research_states.seq of the run when the write happened, to tie it to the turn
--   reason     what wrote it: notes_append, notes_update, notes_scratch, consolidation, restore (the loop restored the file)
--   text       the whole notes.md after the write
CREATE TABLE IF NOT EXISTS gb_agent_notes_versions (
	run_id     INTEGER NOT NULL REFERENCES gb_agent_research_runs(id) ON DELETE CASCADE,
	seq        INTEGER NOT NULL CHECK (seq >= 1),
	state_seq  INTEGER NOT NULL DEFAULT 0,
	reason     TEXT NOT NULL DEFAULT '',
	text       TEXT NOT NULL,
	created_at DATETIME NOT NULL,
	PRIMARY KEY (run_id, seq)
);
```

- The versions of a run are kept until the run is deleted. Consolidation keeps each copy near `notes_max_tokens`, so the table grows by about one such copy per write.

## Observability

Each iteration is a recorded state with the question id in its `detail`, as today. The admin UI and the ATIF trajectory can render the frontier as a tree (`parent_id`) with statuses, from the tables above; the transitions table gives the history of every question, one row per status change, so the path of the search (what was selected when, what was retried, reopened or pruned and why) can be replayed and charted; typical reads: time spent per status, questions that were reopened, resolved-vs-unresolvable ratio per depth, what the code (versus the agent) pruned. The challenges table is the record of how the agent tests itself; typical reads: challenges per target and per strategy, outcome distribution (a run where every challenge `survived` with one strategy is suspect), refutations with their sources, and for each `refuted`/`weakened` outcome the claims and questions that changed afterwards (`result_claim_id`, `gb_agent_frontier_transitions`, where `caused_by` holds the challenge id). Each challenge is also a state (`challenging`) of the run. Totals (`steps`, `tool_calls`, tokens, cost) keep their meaning; `steps` now counts turns.

## Configuration

New knobs, in the [agent definition](agent-defs.md) and as run options (with these defaults); none changes the behaviour of an agent definition that does not set them except `loop = "investigative"`, which turns the whole design on (the existing plain loop remains as `loop = "simple"` during the migration):

`research_notes_dir` (server config, default in the research data directory) · `max_depth` 3 · `max_attempts` 4 · `max_questions` 40 · `max_iterations` 60 · `max_total_tokens` · `max_cost_usd` · `synthesis_reserve` 0.15 · `keep_turns` 2 · `extraction_threshold` 4000 chars · `notes_max_tokens` 6000 · `min_sources` 5 · `min_domains` 3 · `min_independent_sources` 2 · `min_challenges` 1 · `max_challenges_per_target` 3 · `gate_priority` 0.5 · `max_reopens` 1 · `max_parallel` 1.

## Out of scope (for the first implementation)

- Notes in the DB or an object store (the interface allows it).
- More than one worker on the frontier.
- Semantic dedup of questions with embeddings (a normalized string comparison first).
- Automatic assessment of source independence beyond domain and the agent's declaration.
- Changes to `web_search`'s own server-side behaviour.

## Delivery order

The steps below are implemented in two stages. [Stage 1](deep-research-loop-stage-1.md) covers the pure core, the schema and store, and the frontier, notes and challenge tools, with no change to the loop. [Stage 2](deep-research-loop-stage-2.md) covers the investigative loop, the gates, the breakers, the observability, the knobs and the evals. Each stage page says what to build and how to know it is done.

1. Pure core: frontier, claim and notes values and transitions; selection, pruning, gates, breaker checks (with tests, no model).
2. Schema (both backends), store functions, `docs/listings/db-tables.md`.
3. Tools (frontier, notes, `fetch_page`, `finish_exploration`, `finalize_report`) and the effect layer.
4. The exploration loop with prompt rebuilding and eviction, then the synthesis loop.
5. Breakers and reserve, states, observability, agent-definition knobs.
6. Evals: compare against the `simple` loop on the same topics (cost, number of verified claims, gaps reported).
