# Temporal edge cases

The rules `remember` follows for the dates of facts, in one place. The model (event time and system time, the pipeline, the invalidation rule) is in [temporal.md](temporal.md); this page lists the special cases and what happens in each.

Code: `Extract` and `fixSpan` in `internal/memory/memory_remember/remember.go`, the prompt in `prompts_remember.go` (`entitySystemPrompt`, `resolveSystemPrompt`), `resolveEdges`, `storeEdges` and `contradict` in `temporal.go`.

Terms: a fact is **live** while it has no `invalid_at` and no `expired_at`; it **ended** when it has an `invalid_at` (the text said so); it is **superseded** when it has an `expired_at` (a later fact closed it).

## 1. Reading the dates of the text (Extract)

| Case | What happens |
| ---- | ------------ |
| The text states no start (present tense: "Alice works at Acme"; "Alice started working at Globex" with no date) | `valid_at` is the artifact's reference time (`reference_time`, default now). |
| Relative dates ("yesterday", "last year", "two weeks ago") | The prompt tells the model to resolve them against the reference time. |
| Partial dates | The earliest instant: a month becomes the 1st at 00:00:00Z, a year becomes 1 January. There is no precision field. |
| An unparseable date, or an empty one | Counts as missing: a missing `valid_at` becomes the reference time, a missing `invalid_at` means "still true". |
| The text does not say the fact ended | `invalid_at` stays null. Nothing is inferred from, for example, a description such as "plans to leave". |

## 2. A fact that ended

| Case | What happens |
| ---- | ------------ |
| The text gives an end and a start before it ("Alice worked at Acme from 2020 until June 2024") | Both are stored as given: `2020-01-01 -> 2024-06-01`. |
| The text gives an end and no start ("Alice left Acme in June 2024") | The prompt says `valid_at` = `invalid_at`. The result is an **empty span** (`valid_at` equals `invalid_at`): "start unknown, ended then". `valid_at` is `NOT NULL` in the database, so an unknown start can't be stored as null. |
| The model sets a start after the end, usually the reference time it defaulted to (for example `valid_at` = 2024-09-10, `invalid_at` = 2024-06-01) | `fixSpan` keeps the end and moves `valid_at` back to `invalid_at` (the same empty span). The end is more reliable than a start the model defaulted. Before 0.140.0 the end was dropped and the fact was stored as still true. |
| The model sets an end equal to the start | Kept as it is (an empty span). Before 0.140.0 it was dropped as "inconsistent". |

An empty span is not an error. Reconciliation treats it like any ended fact: it never overlaps a later fact (see section 4), and `query` shows it as having stopped being true at that instant.

## 3. Moves, replacements and plans

| Case | What happens |
| ---- | ------------ |
| A completed move or departure ("X has moved its back office from Wiesbaden to Darmstadt in August 2024") | Two edges: X is in Darmstadt (`valid_at` = the move date, live) and X is in Wiesbaden (`invalid_at` = the move date). The prompt asks for this. The old edge is **ended**, not superseded: it has no `expired_at`. |
| A replacement ("Elena Fischer replaced Reto Steiner as CEO") | The old holder's edge ends at that date, the new holder's starts. |
| A planned, future change ("will move ... by the end of 2025") | The old fact has **not** ended, so its edge stays live. Do not write a case that expects it ended: reword the text as a completed move, with a reference time after it. |
| The move is stated in a later text than the old fact | Handled by contradiction detection instead (section 4): the new fact closes the stored one. |

## 4. New facts against stored ones (resolve and store)

| Case | What happens |
| ---- | ------------ |
| Exact repeat (same normalized source, target and relation as a candidate that has no `invalid_at`) | Found in Go, no LLM call. It is a duplicate: not inserted again, the new artifact is recorded in `gb_edge_artifacts`. |
| A duplicate whose text says it ended | The stored edge gets that `invalid_at` (`COALESCE`: an end already stored is never overwritten). `expired_at` is not set: the fact ended, nothing superseded it. |
| The same fact worded differently | The LLM flags it as a duplicate; it is merged the same way. |
| A new fact that cannot be true together with a live stored one ("Alice works at Globex" vs "Alice works at Acme") | The LLM flags a contradiction. The stored fact is closed: `invalid_at` = the new fact's start, `expired_at` = now. It is **superseded**. |
| The two periods do not overlap (the old one ended at or before the new start, or the new one ended at or before the old start) | Nothing is changed. This includes an empty span. |
| The new text is about an **earlier** time than what is stored (the new fact starts before the stored one) | The new fact is the superseded one: closed with `invalid_at` = the stored fact's start and `expired_at` = now. |
| A fact that stopped and later became true again | A new row: there is no uniqueness on (source, target, relation). |
| No stored fact touches the entities of the new ones (the first text of a dataset) | There is nothing to resolve (the resolve call is skipped). A fact the text says nothing about ending stays live. |
| Several chunks of one document | Chunks go in order, one transaction each, so a later chunk can close or merge with a fact of an earlier one. |

Resolution only sees the live edges that share an entity with the new ones (by name, and by similarity with vectors), the 50 newest. A stored fact outside that set can't be merged or closed.

## 5. What `query` sees

| State of the edge | Retrieved? | Notes |
| ----------------- | ---------- | ----- |
| Live | yes | Answers "now" questions. |
| Ended (`invalid_at`, no `expired_at`) | yes | Shown with `valid_at` and `invalid_at`; answers history ("where was it before?") and must not be presented as current. This is the state of the Wiesbaden edge in a completed move. |
| Superseded (`expired_at`) | no | Kept in the database, not retrieved: a question about what used to be true can't be answered from it. |

Details: [query.md](query.md#facts-over-time-and-history).

## 6. Writing tests and evals for these cases

- Use the past tense and a reference time after the event to test that a fact **ended** ("has moved ... in August 2024", reference time in September 2024). A future tense yields a live fact by design (section 3).
- A single text can end a fact only through an `invalid_at` the extractor sets (sections 2 and 3). `expired_at` is set only when a later `remember` supersedes a stored fact (section 4), so a fact that is superseded needs two texts.
- In an eval's `expect_edges`, `"ended": true` checks that the edge has an `invalid_at` (and `valid_at` / `invalid_at` check the dates when given). `"superseded": true` additionally checks that it has an `expired_at`: use it only in a case that remembers a second, contradicting text (like `job_change`). Before 0.140.0 `ended: true` also required an `expired_at`, which a single-text case can never satisfy; see [evals/knowledge.md](evals/knowledge.md).
