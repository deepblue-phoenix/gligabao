# Sessions

A **session** is one run of a user or an agent: a conversation, a task, a crawl. `remember` and `recall` take an optional `session_id`, a string the caller chooses, which ties together everything that came from that run: the artifacts it remembered, the entities and facts those artifacts yielded, and the recalls it made.

A session is only a label. It is not a store: there is no session table, no expiry, no cache of turns, and `recall` gives the same answer with or without it. (For what a Cognee-style session memory would add, and why it is left out, see [not-included.md](not-included.md#session-memory).)

Code: `internal/memory/memory_core` (`ResolveSessionID`, `WithSessionID`), `internal/memory/memory_remember` (`Store`), `internal/db/graph/{sqlite,postgres}/db_schema.go` (tables).

## The `session_id`

- Optional, on `POST /v1/gb/remember` (text and image) and `POST /v1/gb/recall`. Absent or empty means no session.
- Chosen by the caller and used as given after trimming whitespace. At most 128 bytes, no control characters; anything else returns `400` before anything is stored or any LLM is called.
- Not scoped to a user or dataset by gligabao: `"s1"` of two users is two unrelated sessions in practice, because the artifacts, entities and edges it labels live in their own datasets. Pick IDs that are unique for the caller (a UUID, say).

In Go, pass `memory_core.WithSessionID(id)` as an option to `memory_remember.Remember`, `memory_remember.Store`, `memory_image.RememberImage` and `memory_recall.Recall`. Validate user input with `memory_core.ResolveSessionID` first (the functions do too, and return `ErrInvalidSession`).

## What `remember` records

| What | Where | When |
| ---- | ----- | ---- |
| The artifact | `gb_artifacts.session_id` (NULL if none) | Always: every `remember` call adds one artifact |
| Entities discovered in the session | `gb_entity_sessions` (`entity_id`, `session_id`) | For every entity the artifact mentions: created now, **or already stored** |
| Facts (edges) discovered in the session | `gb_edge_sessions` (`edge_id`, `session_id`) | For every edge the artifact states: created now, **or an already stored one that this artifact repeats** |
| An image artifact | `gb_image_artifacts.session_id` (NULL if none) | Always, see [images.md](images.md) |

The two link tables are the point of the feature. Entities and edges are deduplicated: if session B says "Alice works at Acme" after session A did, no entity or edge row is written for B (only B's artifact). Without the link tables the graph would say it was discovered by A alone. With them, `gb_entity_sessions` holds (Alice, A), (Alice, B), and `gb_edge_sessions` holds (works_at, A), (works_at, B), so you can answer "in which sessions did this entity/fact come up?" and, the other way, "which entities and facts did this session touch, including ones that were already known?".

Details:

- Both links are idempotent (primary key on the pair): several artifacts of one session, or several mentions inside one artifact, add one row. Deleting an entity or edge removes its links. Sessions have no rows of their own, so `session_id` is a plain string, not a reference.
- A call without a session adds no row to either link table.
- An entity counts as discovered in the session in the same cases as for [`gb_entity_artifacts`](artifacts.md#provenance-links): the extraction lists it, or an edge names it (even if the extraction did not list it and it is already stored).
- A contradicted edge is not linked to the session of the artifact that contradicted it: a contradiction is not a restatement (same rule as `gb_edge_artifacts`).
- The link is made in the same transaction as the artifact, so nothing session-related is stored if `remember` fails.
- The session link of an entity is independent of the artifact link: it would survive even if the link to an artifact went away.

Example. Sessions `a` and `b` both remember "Alice works at Acme", then a third call without a session does:

| Table | Rows |
| ----- | ---- |
| `gb_artifacts` | (1, `a`), (2, `b`), (3, NULL) |
| `gb_entities` | Alice, Acme (written once, by artifact 1) |
| `gb_entity_sessions` | (Alice, `a`), (Alice, `b`), (Acme, `a`), (Acme, `b`) |
| `gb_edges` | one edge, `artifact_id = 1` |
| `gb_edge_sessions` | (edge, `a`), (edge, `b`) |

## What `recall` does with it

`recall` only carries the session:

- it is echoed in the response as `session_id` (omitted when there was none);
- in an eval call ([evals.md](evals.md)), the ATIF trajectory's `session_id` is set to it, instead of the trajectory's own ID, and `extra.input` has it. The trajectories of `remember` and `recall` calls from the same session then share a `session_id`, which is how ATIF intends to group them;
- it is part of the `remember.*` and `recall.*` monitoring events and the log lines of the calls.

It does **not** filter or boost retrieval, and the answer does not depend on it: `recall` stays a function of the graph and the query. Nothing about the recall is written to the database. To connect a recall to the artifacts of its session, use the same `session_id` on both and join on it (the artifacts through `gb_artifacts.session_id`, the entities and facts through the link tables, the recall through the response, monitoring event or trajectory).

## Reading it back

There is no endpoint for it yet (as for the artifact link tables, see [artifacts.md](artifacts.md#limitations)). In Go, `graph.ListArtifacts` returns `SessionID`, and `graph.ListEntitySessions` and `graph.ListEdgeSessions` return the links of a dataset. In SQL:

```sql
-- sessions an entity was discovered in
SELECT session_id FROM gb_entity_sessions WHERE entity_id = ?;
-- entities a session touched
SELECT e.* FROM gb_entities e JOIN gb_entity_sessions s ON s.entity_id = e.id WHERE s.session_id = ?;
```

## Limitations

- Existing database files are not migrated: the `session_id` columns and the two link tables exist only in a database created by this version (delete the file and the server recreates it).
- An image the dataset already holds is answered from its stored extraction and adds no artifact, so a repeat from another session is not recorded; the response shows the session of the artifact that first stored it (see [images.md](images.md#repeated-images)).
- Not exposed by the API: the [graph export](graph.md) and the recall `evidence` do not list sessions yet.
- A session cannot be listed, closed or deleted: it exists only as the label on the rows above.
