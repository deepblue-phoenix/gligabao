# Temporal facts

gligabao follows the bi-temporal model of [Graphiti](https://github.com/getzep/graphiti).

Code: `internal/memory/memory.go` (`Extract`, `Remember`, `Store`), `internal/memory/temporal.go` (reconciliation), `internal/db/graph/{sqlite,postgres}/db_schema.go` (tables).

## Artifacts

See [artifacts.md](artifacts.md) for the full picture. Every input to `POST /v1/gb/remember` is stored as an **artifact** (a `gb_artifacts` row), a piece of data ingested to be remembered. The request's `kind` field picks the kind: `knowledge` (the default; knowledge that is not strictly episodic) or `episode` (a Graphiti-style episode, anchored in time). The kind is stored but does not change extraction or reconciliation yet: both kinds get the same bi-temporal treatment.

| Column | Meaning |
| ------ | ------- |
| `kind` | `knowledge` (default) or `episode` |
| `text` | The raw input |
| `valid_at` | Event time: when the text was written or observed. This is the request's `reference_time` (default: now). Relative dates in the text are resolved against it. |
| `created_at` | System time: when it was ingested |

## Facts (edges)

Entities (`Alice`, `Acme`) are connected by **edges** (`Alice --works_at--> Acme`), each one a fact with a sentence `description` ("Alice works at Acme.") and two timelines:

| Timeline | Columns | Meaning |
| -------- | ------- | ------- |
| Event time (the world) | `valid_at`, `invalid_at` | When the fact became true / stopped being true. `invalid_at` is `NULL` while the fact is still true. |
| System time (our knowledge) | `created_at`, `expired_at` | When we learned about it / when newer knowledge superseded it. `NULL` `expired_at` means the fact is live. |

Each edge also has `artifact_id`, the artifact it was extracted from. Every artifact that states the edge is also recorded in `gb_edge_artifacts`, and entities are linked to artifacts in `gb_entity_artifacts` (see [remember.md](remember.md#schema)).

All timestamps are RFC 3339 UTC strings (`2006-01-02T15:04:05Z`).

`valid_at` is always set: it is the date the text states, or the artifact's `valid_at` when the text gives none ("Alice works at Acme", "Alice started working at Globex"). `invalid_at` is only set when the text says the fact ended ("Alice left Acme in June 2024"). Partial dates use their earliest instant (a month becomes the 1st, a year becomes 1 January). Unparseable dates count as missing, and an `invalid_at` that is not after `valid_at` is discarded.

There is no uniqueness on (source, target, relation): a fact can end and later become true again, which is a new row.

## Pipeline

Run by `memory.Remember`:

1. **Extract** (`Extract`, 1 LLM call): the model receives the reference time and the text and returns entities and edges with `description`, `valid_at` and `invalid_at`.
2. **Find candidates** (`loadCandidates`): live (not expired) stored edges that touch at least one entity named in the new edges (matched by normalized name), newest first, at most 50.
3. **Resolve** (`resolveEdges`):
   - *Exact repeats* (same normalized source, target and relation as a candidate that has no `invalid_at`) are detected in Go, with no LLM call.
   - The rest are sent to a second LLM call together with the candidates. It returns `duplicates` (same fact, different wording) and `contradictions` (an existing fact can't be true at the same time as a new one). IDs and indexes that weren't in the prompt are dropped.
   - If there are no candidates or nothing is left after the exact-repeat check, this call is skipped.
4. **Store** (`Store`), in one transaction: the artifact, entities, entity-artifact links and edges are written.
   - Duplicates are not inserted again; the existing edge just gets an `invalid_at` if the text says it ended.
   - New edges are inserted with `created_at` = now.
   - Contradictions are applied with the invalidation rule below.

### Invalidation rule

For a new fact N contradicting an existing live fact O (N's start is its `valid_at`):

- If O already ended at or before N's start, or N ended at or before O's start: the periods don't overlap, nothing changes.
- If O started at or before N's start: O is closed with `invalid_at` = N's start and `expired_at` = now.
- If O started after N's start (we received information about an earlier time than what we already know): N is the superseded one, closed with `invalid_at` = O's start and `expired_at` = now.

The closed edge's ID is reported in `invalidated_edges`.

Example: "Alice works at Acme" (artifact at 2023-01-01), then "Alice started working at Globex" (artifact at 2024-03-01):

| id | fact | valid_at | invalid_at | expired_at |
| -- | ---- | -------- | ---------- | ---------- |
| 1 | Alice works at Acme. | 2023-01-01 | 2024-03-01 | (time of the second `remember`) |
| 2 | Alice works at Globex. | 2024-03-01 | | |

## Recall

`recall` sends every retrieved edge to the LLM with its `valid_at` and `invalid_at` (and a "superseded" marker) plus the current time. The model is told that edges without `invalid_at` are still true, so "Where does Alice work now?" is answered from the Globex edge, while history questions can use the closed Acme edge. Evidence edges carry the temporal fields too.

## Errors and cost

- Any failing LLM step (extraction, resolve) returns `502` and stores nothing.
- `remember` makes 1 LLM call, or 2 when there are stored facts to compare against.

## Limitations

- Contradiction detection is limited to live edges sharing an entity with the new edges, and to the 50 newest.
- A duplicate edge is merged into the stored one; the additional artifact it was seen in is recorded in `gb_edge_artifacts`, but the stored edge keeps its own `artifact_id`, `created_at` and `valid_at`.
- Dates have no precision field: a month-only date is stored as its first instant.
- Timestamps have one-second resolution.
