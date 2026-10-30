# Edge events

`gb_edges` holds the **final** state of a fact. It cannot say what a `remember` call did to it: a closed fact keeps no trace of its old `invalid_at`, and a fact that was merely stated again leaves only a link in `gb_edge_artifacts`. The **edge event log**, `gb_edge_events`, keeps the steps: an append-only table, one row for each fact a call touched, written in the same transaction as the artifact. It is what the admin UI plots in the [Temporal changes](remember.md#admin-ui) view.

The model of the two timelines and the invalidation rule are in [temporal.md](temporal.md); this page is only about the log.

Code: `storeEdges` and `contradict` in `internal/memory/memory_remember/temporal.go` (the writers), `model.EdgeEvent` in `internal/db/graph/model/model.go`, `InsertEdgeEvent` and `ListEdgeEvents` in `internal/db/graph/{sqlite,postgres}/db.go`, the endpoint in `internal/api/admin/remember_runs.go` (`RememberRunTemporalOf`).

## The table

| Column | Meaning |
| ------ | ------- |
| `id` | Events are read in the order of `id`, which is the order they happened |
| `dataset_id` | → `gb_datasets` |
| `edge_id` | The fact, → `gb_edges` (deleting the edge deletes its events) |
| `artifact_id` | The artifact whose `remember` did it, → `gb_artifacts` (a chunk, a web search result and a series observation are artifacts of their own). `NULL` if the artifact was deleted: the event stays |
| `event` | What happened, see below |
| `invalid_at_before`, `invalid_at_after` | The fact's `invalid_at` before and after the event; `NULL` is open-ended (still true) |
| `caused_by_edge_id` | The other fact of a contradiction, → `gb_edges`; `NULL` for the other events (and if that edge was deleted) |
| `created_at` | System time of the call. For `invalidated` and `superseded` it is also the edge's `expired_at` |

`valid_at` never changes once an edge is inserted, so the edge's `valid_at` together with `invalid_at_before` and `invalid_at_after` is the whole before and after of the fact in event time. The table is created automatically in an existing database (`CREATE TABLE IF NOT EXISTS`, no migration); calls made before it existed have no events.

## Events

| `event` | When it is written | `invalid_at_before` → `invalid_at_after` | `caused_by_edge_id` |
| ------- | ------------------ | ---------------------------------------- | ------------------- |
| `created` | The call stored the fact as a new edge | none → the `invalid_at` the text gave, if any | |
| `confirmed` | The call stated a fact that is already stored (an exact repeat, or a duplicate the LLM found); nothing changed | the stored value, twice | |
| `ended` | Same, and the text gave the stored fact an end while it had none: its `invalid_at` was set | none → the end | |
| `invalidated` | A stored fact contradicted by a new one was closed (`invalid_at` and `expired_at` set) | the old value → the start of the new fact | the new fact |
| `superseded` | The new fact was itself closed at once, because it is about an earlier time than the stored fact it contradicts | its own value → the start of the stored fact | the stored fact |

Facts of the call that need no change (a repeat inside the same artifact, a contradiction whose periods do not overlap) write nothing. One fact can have two events in one call: `created`, then `superseded`.

## Example

"Alice works at Acme" (reference time 2023-01-01, run A), then "Alice started working at Globex" (2024-03-01, run B), as in [temporal.md](temporal.md#invalidation-rule):

| id | edge | artifact | event | `invalid_at_before` | `invalid_at_after` | `caused_by_edge_id` |
| -- | ---- | -------- | ----- | ------------------- | ------------------ | ------------------- |
| 1 | 1 (Acme) | A | `created` | | | |
| 2 | 2 (Globex) | B | `created` | | | |
| 3 | 1 (Acme) | B | `invalidated` | | 2024-03-01 | 2 |

## Reading it

- Go: `graph.ListEdgeEvents(ctx, db, artifactID)` returns the events of the artifact **and of every artifact below it** through `parent_id` (the chunks of a long text, its web searches and series observations), oldest first, each with its fact in its current state (`Edge`, with the endpoint names).
- Admin UI endpoint: `GET /v1/gb/admin/api/remember-run-temporal?run_id=<id>[&db=<name>]`, described in [remember.md](remember.md#admin-ui). It uses the artifact of the run (`gb_remember_runs.artifact_id`), so a run that stored nothing has no events.
- SQL, for the history of one fact: `SELECT * FROM gb_edge_events WHERE edge_id = ? ORDER BY id` (indexed, like `artifact_id`).

## Limitations

- Only facts are logged. Entities have no `valid_at` or `invalid_at`, so nothing about them can be invalidated.
- The log records what `remember` does. A fact that is closed or deleted some other way (SQL, deleting a dataset) gets no event.
- The log is not pruned: it grows with the number of facts a call touches. Deleting a dataset's edges removes their events.
