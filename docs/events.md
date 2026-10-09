# Temporal events

Alongside the entity/edge graph, `POST /v1/gb/remember` extracts **events** (things that happened, started, ended, changed or were planned) and stores them in their own tables with a bi-temporal model, the same one [Graphiti](https://github.com/getzep/graphiti) uses for its edges. When new information contradicts an older event, the older one is closed rather than deleted, so the history stays queryable.

Code: `internal/memory/events.go` (pipeline), `internal/memory/memory.go` (`Remember`, `Store`), `internal/db/db_schema.go` (tables).

## Bi-temporal model

Every event has two independent timelines:

| Timeline | Columns | Meaning |
| -------- | ------- | ------- |
| Event time (the world) | `valid_at`, `invalid_at` | When the event started being true / stopped being true. `NULL` means unknown (`valid_at`) or still true (`invalid_at`). |
| System time (our knowledge) | `created_at`, `expired_at` | When we learned about it / when newer knowledge superseded it. `NULL` `expired_at` means the event is still live. |

All timestamps are stored as RFC 3339 UTC strings (`2006-01-02T15:04:05Z`).

Example: "Alice joined Acme in 2020" (learned in 2024), later followed by "Alice joined Globex in June 2023", yields

| id | description | valid_at | invalid_at | expired_at |
| -- | ----------- | -------- | ---------- | ---------- |
| 1 | Alice joined Acme. | 2020-01-01 | 2023-06-01 | (time of the second `remember`) |
| 2 | Alice joined Globex. | 2023-06-01 | | |

## Request

Same as [remember](remember.md), plus an optional field:

```json
{"text": "Alice joined Globex last June.", "reference_time": "2024-01-31T09:00:00Z"}
```

- `reference_time` (optional, RFC 3339): when the text was written or observed. Relative expressions ("yesterday", "last year") are resolved against it. Defaults to the current time. A malformed value returns `400`. It is stored on the memory (`gb_memories.reference_time`).

## Response

The `remember` response gains two fields:

```json
{
  "memory_id": 2,
  "entities": [...],
  "edges": [...],
  "events": [
    {
      "id": 2,
      "name": "joined_company",
      "description": "Alice joined Globex.",
      "participants": ["Alice", "Globex"],
      "valid_at": "2023-06-01T00:00:00Z",
      "created_at": "2025-01-01T12:00:00Z"
    }
  ],
  "invalidated_events": [1]
}
```

- `events`: the events from this text in their final stored state. If a new event repeated an existing one, the existing event is returned (with any missing dates filled in).
- `invalidated_events`: IDs of events closed because of this memory (see below). Always an array.
- Event fields `valid_at`, `invalid_at`, `created_at`, `expired_at` are omitted when null.

## Pipeline

Run by `memory.Remember` after entity/edge extraction:

1. **Extract** (`ExtractEvents`): an LLM call (temperature 0) receives the text, the reference time and the entities found in the same text, and returns events with `name`, `description`, `participants`, `valid_at` and `invalid_at`.
   - Dates are never guessed: unknown means `null`.
   - Partial dates use their earliest instant (a month becomes the 1st, a year becomes 1 January).
   - If something stopped ("Alice left Acme in June 2024"), the event describes the thing that stopped ("Alice worked at Acme") with `invalid_at` set.
   - Normalization: names become `snake_case` (empty becomes `event`); events without a description are dropped; participants are de-duplicated; unparseable dates become null; an `invalid_at` that is not after `valid_at` is discarded.
2. **Find candidates** (`loadCandidates`): live (not expired) stored events that share at least one participant (matched by normalized entity name) with the new events, newest first, at most 50.
3. **Resolve** (`resolveEvents`):
   - *Exact repeats* (same normalized description and same participant set) are detected in Go, with no LLM call.
   - The rest are sent to a second LLM call together with the candidates. It returns `duplicates` (same fact, different wording) and `contradictions` (an existing event can't be true at the same time as a new one). IDs and indexes that weren't in the prompt are dropped.
   - If there are no candidates or nothing is left after the exact-repeat check, this call is skipped.
4. **Store** (`storeEvents`), in the same transaction as the memory, entities and edges:
   - Duplicates are not inserted again; the existing event just gets any missing `valid_at`/`invalid_at` filled in.
   - New events are inserted into `gb_events` and linked to their participants in `gb_event_participants`. A participant is an entity from this extraction, else an already stored entity of any type with the same name, else a new entity of type `other`.
   - Contradictions are applied with the invalidation rule below.

### Invalidation rule

For a new event N contradicting an existing live event O (N's start is `valid_at`, or the memory's reference time if unknown):

- If O already ended at or before N's start, or N ended at or before O's start: the periods don't overlap, nothing changes.
- If O started at or before N's start (or O's start is unknown): O is closed with `invalid_at` = N's start and `expired_at` = now.
- If O started after N's start (we received information about an earlier time than what we already know): N is the superseded one, closed with `invalid_at` = O's start and `expired_at` = now.

The closed event's ID is reported in `invalidated_events`.

## Schema

| Table | Columns |
| ----- | ------- |
| `gb_events` | `id`, `name`, `description`, `description_key` (normalized, for dedup), `valid_at`, `invalid_at`, `created_at`, `expired_at`, `memory_id` → `gb_memories` (set to NULL if the memory is deleted) |
| `gb_event_participants` | `event_id` → `gb_events`, `entity_id` → `gb_entities`; primary key (`event_id`, `entity_id`); both cascade on delete |
| `gb_memories` | gains `reference_time` |

Existing database files are not migrated. Delete the file (default `bin/gligabao.db`) and the server recreates it.

## Errors and cost

- Any failing LLM step (extraction, event extraction, resolve) returns `502` and stores nothing.
- `remember` makes 2 LLM calls, or 3 when there are existing events to compare against.

## Limitations

- `recall` does not use events yet.
- Contradiction detection is limited to events sharing a participant, and to the 50 newest live candidates.
- Duplicates are matched exactly or by the LLM; a duplicate does not record the additional memory it was seen in.
- Dates have no precision field: a month-only date is stored as its first instant.
