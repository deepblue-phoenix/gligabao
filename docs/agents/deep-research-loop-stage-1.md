# Deep-research loop, stage 1: state, storage and tools

**Status:** implemented in 0.180.0 (see [Implementation notes](#implementation-notes) for where the code differs from or extends this page). This is the first of two implementation stages of the [deep-research loop](deep-research-loop.md). The design page defines the concepts, the data and the rules; this page says what to build in this stage, in what order, and how to know it is done. Stage 2 is [deep-research-loop-stage-2.md](deep-research-loop-stage-2.md).

Read the design page first, in full. Where this page is narrower than the design, this page wins for this stage; where it seems to contradict the design, stop and raise it rather than choose.

## Goal

Build everything the investigative loop needs to **hold and change its state**, without the loop itself:

- the question frontier, the notes, the claim ledger and the challenge records as **pure values with pure transitions**;
- their tables in both database backends, and the store functions that write them atomically;
- the **tools** the model will call (frontier, notes, challenges), with their validation, behind a registry that no agent uses yet.

At the end of this stage a test can drive a whole frontier, notes and challenge history through the tools with a fake model and an in-memory or SQLite store, and get the same state the design describes. Nothing in an agent run changes: the existing loops and the existing tools behave exactly as before.

## Out of scope for this stage

- The investigative loop, the turn builder, prompt rebuilding and eviction (stage 2).
- The forced extraction and the extraction-threshold check (stage 2). `fetch_page` is not renamed here.
- The finalization gate, `finish_exploration`, `finalize_report`, the synthesis loop, the breakers and the synthesis reserve (stage 2).
- New `gb_agent_research_runs` columns (`phase`, `stop_reason`) and the new `gb_agent_research_states` values (stage 2, they are only read by the loop).
- Agent-definition and run-option knobs (stage 2). Stage 1 uses the design defaults as Go constants.
- Logging of web search and fetch attempts from the existing tools (stage 2 wires them when the loop runs).
- Admin UI, ATIF rendering and the trajectory of the frontier (stage 2 or a leftover task).

Anything from the design's "Out of scope" section stays out.

## Ground rules

- **Functional style is mandatory** (see `gligabao/AGENTS.md`). The frontier, notes, claims and challenges are immutable values. Every change is a pure function `(value, input) -> (value, effects-to-record, error)`. Reads and writes to the DB and the notes file happen only in the effect layer.
- **Code decides control, the model decides content.** Selection order, depth cap, attempt cap, dependency checks, DAG check, duplicate check and the challenge rules are code. Wording, queries and answers come from the model.
- **Schema is SQL.** New tables go into `internal/agent_research/db/sqlite/schema.sql` and `internal/agent_research/db/postgres/schema.sql` (same tables, comments in the SQLite file). Both files are idempotent (`CREATE ... IF NOT EXISTS`) and in dependency order. Plain SQL only, no string splicing.
- **Migrations for existing tables.** A change to a table that already exists is an `ALTER` that runs on every startup, as the design's [Migrations](deep-research-loop.md#database-schema) rules say (item 10 shows the stage 1 case).
- **Versioning:** a new schema is a minor bump of `VERSION`, with a `CHANGELOG.md` entry (see `gligabao/AGENTS.md`). Keep `docs/listings/db-tables.md` up to date with every new table.
- **Tests first for the pure core.** Each pure function gets unit tests that need no model and no DB.

## Recommended layout

The design does not fix the code layout; this is the layout to use unless something in the repo argues against it:

| Path | Content |
| ---- | ------- |
| `internal/agent_research/investigate/` | pure core: `frontier.go`, `notes.go`, `claims.go`, `challenges.go`, `ids.go`, `limits.go` (design defaults as constants) |
| `internal/agent_research/investigate/tools.go` | tool specs (name, JSON schema, description) and the pure handlers `(state, args) -> (state, result, effects)` |
| `internal/agent_research/investigate/store.go` | the `Store` interface the effect layer needs (no DB types) |
| `internal/agent_research/investigate/notes_file.go` | the notes file behind a small interface (`Read`, `Write`), file implementation |
| `internal/agent_research/db/sqlite/` and `db/postgres/` | the new tables in `schema.sql`, and the `Store` implementation in `store.go` (or a sibling file of the same package) |
| `internal/agent_research/db/db.go` | wiring only, if the package needs it |

Test files sit next to the code (`*_test.go`), following the existing `internal/agent_research/*_test.go` files.

## Work items

Do them in this order; each item is complete (with tests) before the next starts.

### 1. Ids and limits

- `ids.go`: `NextQuestionID`, `NextClaimID`, `NextDatapointID`, `NextSourceID`, `NextChallengeID` take the current set of ids of a run and return the next one (`q_001`, `c_001`, `d_001`, `s_001`, `ch_001`, zero-padded to 3 digits, max existing + 1). Pure. Code assigns ids; the model never does.
- `limits.go`: the defaults from the design's [configuration](deep-research-loop.md#configuration) that this stage uses: `max_depth` 3, `max_attempts` 4, `max_questions` 40, `notes_max_tokens` 6000 (used only by the size check, the consolidation is stage 2), `min_sources` 5, `min_domains` 3, `min_independent_sources` 2, `min_challenges` 1, `max_challenges_per_target` 3, `max_reopens` 1. Use a `Limits` struct with these fields and a `DefaultLimits()` function, so stage 2 can pass run options in.

### 2. Frontier (pure)

File `frontier.go`. The value:

- `Frontier`: questions by id, the dependency edges, the attempt log per question, and the transitions produced so far (or the transitions are returned, not stored in the value; pick one and keep it consistent).
- `Question`: the fields of the design's [question record](deep-research-loop.md#question-record).

Functions, each returning the new value plus the transition(s) it produced:

- `Add(f, parentID, input, limits) (Frontier, Transition, error)`: assigns `id` and `depth` (`parent.depth + 1`, root depth 0); rejects `depth > max_depth`, a normalized near-duplicate of an existing question, and a count above `max_questions`; checks that `depends_on` ids exist and that adding the edges creates no cycle (DFS over the dependency edges; a cycle is an error). A child that unblocks the current question gets priority at least that of the current question, as the design's branching rule says.
- `Select(f) (Frontier, *Question, []Transition)`: the pure selection of the design's [selection](deep-research-loop.md#selection) rule. Among `pending` questions with all dependencies `resolved`: highest priority, then lower depth, then older (lower creation seq). If the top `pending` question is blocked by a dependency, pick the dependency instead, and raise its priority to at least that of its dependant (that change is a `reprioritized` transition). Selecting sets `in_progress`.
- `Resolve(f, id, answer, claimIDs, ...)`, `Retry(f, id, newQuery, why, limits)`, `Reopen(f, id, why, priority)`, `SetPriority(f, id, priority, why)`, `PruneBranch(f, id, reason)`: the design's rules, including the attempt cap (`Retry` at `max_attempts` sets `unresolvable` and requires an assumption), the reopen re-entry, and pruning of pending/blocked descendants recursively with one transition each.
- `Reblock(f)`: after any change, a `pending` question whose dependency is `pruned` or `unresolvable` becomes `blocked`; a `blocked` question whose dependencies are all `resolved` again becomes `pending`. Runs as part of every update (the design's "automatic block or unblock").
- `Summary(f, currentID, limits) string`: the compact frontier text of the design's [prompt contents](deep-research-loop.md#prompt-contents). Stage 2 places it in the prompt; stage 1 only builds and tests it.

Rules for the value:

- Question ids are never reused; a pruned question stays in the value.
- Attempts are derived from the attempt log (`len(attempts)`), not stored separately.
- Dependencies are checked for DAG-ness on every add.

### 3. Transitions (pure)

Part of `frontier.go` (or `transitions.go`). A `Transition` has the fields of the design's [`gb_agent_frontier_transitions`](deep-research-loop.md#database-schema) row. Rules:

- every status change of a question yields exactly one transition, with `seq` = previous `seq` + 1 for that question (creation is seq 1, `from_status` empty);
- a priority-only change yields a transition with `from_status = to_status` and trigger `reprioritized`;
- the closed vocabularies of `trigger` and `actor` are Go constants, and the constructors take only those;
- the latest transition of a question always equals its current status (the test of this invariant is the most important unit test of the stage: run random sequences of operations and check it).

### 4. Notes (pure)

File `notes.go`. The value:

- `Notes`: the fixed sections of the design's [notes.md](deep-research-loop.md#working-state-notesmd) (`Verified`, `Unverified`, `Contradictions`, `Datapoints`, `Sources`, `Assumptions & gaps`, `Scratch`), with their records.

Functions:

- `Parse(text string) (Notes, []error)`: reads the fixed grammar; unknown lines are an error, not silently dropped. `Scratch` is kept as free text.
- `Render(n Notes) string`: the exact inverse. Test: `Render(Parse(x))` equals a canonical form of `x`, and `Parse(Render(n)) == n`.
- `Append(n, section, entries) (Notes, error)`: assigns ids (`c_`, `d_`, `s_`) with the functions of `ids.go`; checks the section accepts the entry kind.
- `Update(n, id, change) (Notes, error)`: the only way to change a record's status, sources or retraction. Rejects `VERIFIED` unless the claim meets the rule of item 5.
- `SetScratch(n, text) (Notes, error)`: replaces `Scratch`; rejects text above a size bound (the bound is a Go constant here; stage 2 uses it for consolidation).
- `TokenEstimate(n) int`: a simple estimate (characters / 4 is acceptable; say so in the code comment). Used by stage 2's size check.

The notes value never edits the file; the effect layer writes it (item 8).

### 5. Claims and sources (pure)

File `claims.go`. Values: `Claim` (id, text, kind, value, unit, status, question id, source ids with stance) and `Source` (id, url, domain, title, note).

- `Domain(url) string`: the registrable domain (use a public-suffix list if the repo already has one; otherwise a documented simple rule that handles `co.uk`-style suffixes, and a test for it).
- `AddSource(sources, url, title, note) (…, Source, error)`: one row per distinct URL (returns the existing source for a known URL).
- `Support(claim, sourceID, stance, sources)`: adds a link; the independence declaration (the agent's statement that the source is not quoting another source already listed) is an input and is stored.
- `CanVerify(claim, sources, challenges, limits) error`: the **only** check for `VERIFIED`: at least `min_independent_sources` `supports` sources with distinct domains, and at least `min_challenges` closed challenges with outcome `survived` on this claim. Every change of status to `VERIFIED` goes through this function.
- `Refute(claim, sourceID, reason)`, `Retract(claim, reason)`: status changes to `REFUTED` and `RETRACTED`, with the contradiction link.

### 6. Challenges (pure)

File `challenges.go`. Implements the design's [adversarial queries](deep-research-loop.md#adversarial-queries) as value checks. Functions:

- `Open(challenges, input, frontier, claims, limits) (…, Challenge, error)`: checks the rules of the design, in this order: the target exists (`claim` id, `question_answer` question id, or `conclusion`); `strategy` is one of the seven; `negation` and `expected_if_false` are non-empty; the normalized `query` differs from every earlier query of the question and of earlier challenges on the same target; the strategy was not used on the same target before; the count on the target is below `max_challenges_per_target`; the query contains at least one term of the negation or the strategy that the target's text does not contain. Returns the open challenge (status `open`, outcome empty). It runs nothing.
- `Close(challenges, id, outcome, evidence, sourceIDs, resultClaimID) (…, error)`: `outcome` is one of `survived`, `weakened`, `refuted`, `inconclusive`; `weakened` and `refuted` require at least one source id and non-empty `evidence`; `survived` requires an attempt to exist (the challenge's `attempt_id` set); a challenge closes once.
- `ChallengeTargetRules` helpers for the design's loop-level requirements (`RequiresAnswerChallenge(question)` for depth 0 and 1, `OpenChallenges(...)`), used by stage 2's gates.

The `attempt_id` link is set by the effect layer after the search runs; `Open` only validates.

### 7. Tool layer (pure handlers + specs)

File `tools.go`. For each tool of the design's tool tables, a spec (name, description, JSON schema of the arguments) and a handler `(state, args) -> (state, result, effects, error)`. Tools in this stage:

- frontier: `frontier_next`, `frontier_view`, `add_question`, `retry_question`, `resolve_question`, `reopen_question`, `set_priority`, `prune_branch`;
- notes: `notes_read`, `notes_append`, `notes_update`, `notes_scratch`;
- challenges: `challenge`, `record_challenge_result`, `challenges_view`.

Rules:

- Handlers return the **compact summary** of what they changed (the design's rule: the full frontier is never returned), and the error text the model sees is the design's wording (for example, the depth cap error "pruned by depth cap; record it as an open gap in notes instead").
- `frontier_next` is rejected until the root has at least one child or the agent has said the objective is atomic (store that statement as a note in `Scratch`).
- `resolve_question` runs the checks of the design: claim ids exist, a `hypothesis` has a confirming and a refuting attempt (or an explicit reason), each cited claim is at least `UNVERIFIED`, and for depth 0 and 1 an answer challenge is closed or a reason is stored.
- Tools are built from a `Registry` value that can be asked for "the tools of the investigative loop". **No agent definition uses the registry in this stage**; a test asserts that the default agent tool set is unchanged.
- Handlers do not call the store. They return effects (`RecordTransitions`, `InsertAttempt`, `UpsertClaim`, `WriteNotes`, `OpenChallenge`, ...) that the effect layer executes.

### 8. Store and the effect layer

- `store.go` in `db/sqlite` and `db/postgres`: implement the `Store` interface of `investigate/store.go`. The interface has one method per **effect**, and a method to load a run's frontier, notes and claims as values. Each method that changes state runs in one transaction. In particular: updating a question's `status` and inserting its transition row happen in the same transaction, and the transition `seq` is computed in that transaction (max + 1 for the question).
- The effect executor (`investigate/effects.go`): takes the list of effects a handler returned and runs them in order inside one store transaction. A notes write adds its `gb_agent_notes_versions` row in that same transaction; the working-copy file is written after the commit (item 9). A failing effect aborts the whole list.
- Ids for rows of the DB (`gb_agent_frontier_attempts.id`, `gb_agent_challenges`' attempt link) use the autoincrement/BIGSERIAL columns.
- The working-copy file is written after the transaction commits, never inside it. If that write fails, the run's notes version is already stored, so the loop restores the file from it at the next turn (stage 2).

### 9. Notes file and notes versions

Two parts, both per run (the design's [working state](deep-research-loop.md#working-state-notesmd) section has the reasons):

**Working copy, a file.** `notes_file.go`: `NotesFile` with `Path(user, runID, notesID) string`, `Read(path) (string, error)` and `Write(path, text) error`.

- File name: `notes_<user>_<run>_<notes_id>.md`. `<user>` is the run's `user_id` with every character outside `A-Z a-z 0-9 . _ -` replaced by `_`, cut to 64 characters. `user_id` is any string (see `docs/identity.md`), so the sanitizer must be tested with `/`, `..`, spaces, unicode and an empty string (an empty user becomes `anon`, and the rule is documented in the code). `<run>` is `gb_agent_research_runs.id`; `<notes_id>` is a random 128-bit id, lower-case hex (32 characters).
- `notes_id` is made once, when the run is created (`crypto/rand`, in the create path of the runs), and stored in the new run column `notes_id` (see item 10). Runs created before have `notes_id = ''` and get no notes file.
- Directory: the server config key `research_notes_dir`, added in this stage with the default in the research data directory (check how `researcher.go` and the existing run files find their directory, and reuse it; do not invent a new root). The directory is created on first write.
- Writes: temp file in the same directory, then rename over the old file, so a reader never sees half a file.
- A missing file reads as the rendered empty notes (`Render(Notes{})`).

**History, the database.** Every notes write adds a row to `gb_agent_notes_versions` (the table of the design's [schema](deep-research-loop.md#database-schema) section): the whole `notes.md` text after the write, with `seq`, `state_seq`, and `reason` (`notes_append`, `notes_update`, `notes_scratch`, `consolidation`, `restore`). Stage 1 provides the store function `AddNotesVersion(tx, runID, text, reason)` and `LatestNotesVersion(runID)`.

The file is the working copy the loop reads and edits; the versions are the record. Test: a write, a reload and a `Parse` give the same value; a version row is written for every notes effect, in the same transaction as the rows it changes; `LatestNotesVersion` equals the file after a successful write.

### 10. Schema and migrations

**New tables.** Add to **both** `schema.sql` files, in dependency order, the tables of the design's [database schema](deep-research-loop.md#database-schema) section:

- `gb_agent_frontier_questions`, `gb_agent_frontier_deps`, `gb_agent_frontier_attempts`, `gb_agent_frontier_transitions`;
- `gb_agent_sources`, `gb_agent_claims`, `gb_agent_claim_sources`;
- `gb_agent_challenges`, `gb_agent_challenge_sources`;
- `gb_agent_notes_versions`.

Rules:

- copy the DDL from the design; the SQLite file gets the column comments (the design's comments are the spec); the PostgreSQL file gets the same tables with its types (`BIGSERIAL`, `TIMESTAMPTZ`, `BIGINT` for `attempt_id`, as the other PostgreSQL schema files of the package do);
- a new table is applied on every start by `CREATE ... IF NOT EXISTS` in `schema.sql` (the `Schema` of each backend, run by `Open` in `internal/agent_research/ops.go`); it needs no upgrade statement;
- update `docs/listings/db-tables.md` with the nine new tables and a one-line description each.

**Changed table: `gb_agent_research_runs.notes_id`.** This is the one change to an existing table in this stage. It must work on a database that already has the table, so it goes in two places:

1. `schema.sql` (both backends), in the `CREATE TABLE` of `gb_agent_research_runs`: `notes_id TEXT NOT NULL DEFAULT ''` (PostgreSQL: the same), with a comment line in the SQLite file.
2. The upgrade list, run by `Open` on every start:
   - SQLite: an entry in the `Upgrade` list of `db/sqlite/schema.go`, `{"notes_id", "ALTER TABLE gb_agent_research_runs ADD COLUMN notes_id TEXT NOT NULL DEFAULT ''"}`. `Open` already runs an `ADD COLUMN` only when `pragma_table_info` does not list the column.
   - PostgreSQL: one line in `db/postgres/upgrade_columns.sql`: `ALTER TABLE gb_agent_research_runs ADD COLUMN IF NOT EXISTS notes_id TEXT NOT NULL DEFAULT '';`.

Do not add `phase` or `stop_reason` here (stage 2). Do not change any existing `CHECK` in this stage (that would need the SQLite table rebuild of `migrateSQLiteStatus`).

Tests for the migration (both backends, with a database created by the **previous** version's schema, which the test builds from the old DDL, not from the new file):

- starting the server on that database adds `notes_id` and leaves the old rows intact (their `notes_id` is `''`);
- a second start changes nothing and does not fail;
- a fresh database gets `notes_id` from `schema.sql` and the upgrade step is a no-op;
- the new tables exist after the start on the old database;
- a foreign key or check violation is rejected (for example a transition row with an unknown `to_status`, a notes version with `seq = 0`).

## Tests

- Pure core: unit tests per file, no DB, no model. The frontier transition invariant test (item 3) and the notes round trip (item 4) are required.
- Store: one test per backend (SQLite always; PostgreSQL where the repo's existing tests use it) that runs an effect list end to end and reads the values back.
- Migration: the upgrade tests of item 10, on both backends.
- Tool layer: a test per tool with a valid and at least one invalid call, checking the error text.
- Regression: the existing `internal/agent_research` tests pass unchanged, and the default tool set of every agent is the same as before.
- Run the repo's test script (`./run_tests.sh`), which also checks that `VERSION` matches the changelog.

## Done when

- every item above is implemented and its tests pass;
- both schema files create the nine tables, `gb_agent_research_runs.notes_id` exists on new and upgraded databases, and `db-tables.md` lists the tables;
- no agent run behaves differently (the registry is not used);
- the pure core has no imports of the DB, the model or the file system;
- `CHANGELOG.md` and `VERSION` are updated (minor bump: new tables and a new column);
- anything knowingly left out that this page did not list is recorded in `leftover_tasks/` (see `gligabao/AGENTS.md`).

## Hand-off to stage 2

Stage 2 needs from this stage: the frontier, notes, claim and challenge values with their transitions; the `Store` and the effect executor; the tool registry; the limits struct; and the summary and size functions for the prompt. Keep those names stable, and say in the changelog that the loop is not yet connected.

## Implementation notes

Where the code (package `internal/agent_research/investigate`, 0.180.0) chose, extended or differs from this page:

- **Notes value holds the records.** `Notes` contains the claims, datapoints, sources, contradictions and assumptions themselves (the claim and source values of `claims.go` are its records), so there is no second copy to keep in step. The notes **text** (latest `gb_agent_notes_versions` row) is the authority when a run is loaded: the independence declarations, the `check` and `note` of a claim have no column in the claim tables, so they live only in the text. The claim and source tables are written by the same effects as the structured history.
- **Handlers describe, `Diff` derives the effects.** A handler returns the new `State`; `Diff(before, after, transitions, notesReason)` (pure) derives the effects, in foreign-key order. `Session` is the effect layer around the registry (`Start`, `Load`, `Call`).
- **Transitions as returned values.** `Add`, `Resolve`, `Retry`, ... return `[]Transition` (a call can change several questions). A question carries its last `TransSeq`.
- **Added trigger `dependency_added`:** an in-progress question goes back to `pending` when a child that blocks it is added (`add_question` with `blocks_parent`).
- **Extra optional tool arguments:** `frontier_next(atomic_reason)` (the statement that the objective is atomic, stored in `Scratch` as `Atomic objective: ...`), `add_question(blocks_parent)`, `retry_question(assumption)` (required at the attempt cap), `resolve_question(reason)` (replaces a missing hypothesis search or answer challenge; stored in the transition).
- **Root question** is created `in_progress` (the objective is always being worked on); `frontier_next` selects among its descendants and is rejected while another question is in progress.
- **`Retry` semantics:** `new_query` is logged as the next attempt with `why` as its outcome; at `max_attempts` logged attempts the next call makes the question `unresolvable` (assumption required).
- **Hypothesis check** counts the attempts that are the searches of challenges (linked by `LinkAttempt`) as refuting and the others as confirming.
- **Outcomes of a challenge** are applied in `record_challenge_result` (claim `REFUTED` plus a high-priority question and reopening of the questions that cite it, scope added on `weakened`, a question queued for a `weakened` or `refuted` conclusion). The same refutation logic runs when `notes_update` sets `REFUTED`.
- **`Domain`** uses a simple rule (last two labels, three for `co.uk`-style suffixes), documented in the code; the repo has no public-suffix list.
- **`research_notes_dir`** defaults to a `research_notes` directory next to the binary (the repo has no research data directory to reuse).
- **PostgreSQL** is covered by the same tests as SQLite when `POSTGRES_TEST_DSN` is set.
