# POST /v1/gb/remember

Takes a block of text, uses an LLM to extract a small knowledge graph (entities and edges) and temporal events, and stores everything in SQLite. Events have their own bi-temporal tables and pipeline, described in [events.md](events.md).

## Request

```
POST /v1/gb/remember
content-type: application/json

{"text": "Alice works at Acme and lives in Paris.", "reference_time": "2025-01-31T09:00:00Z"}
```

- `text` (required): non-empty string. The request body is limited to 1 MiB.
- `reference_time` (optional): RFC 3339 timestamp of when the text was written or observed. Relative dates in the text ("yesterday", "last March") are resolved against it. Defaults to the current time.

## Response

`200 OK`:

```json
{
  "memory_id": 1,
  "entities": [
    {"name": "Alice", "type": "person", "description": "engineer"},
    {"name": "Acme", "type": "organization"},
    {"name": "Paris", "type": "other"}
  ],
  "edges": [
    {"source": "Alice", "target": "Acme", "relation": "works_at"},
    {"source": "Alice", "target": "Paris", "relation": "lives_in"}
  ],
  "events": [
    {
      "id": 1,
      "name": "joined_company",
      "description": "Alice joined Acme.",
      "participants": ["Alice", "Acme"],
      "valid_at": "2020-01-01T00:00:00Z",
      "created_at": "2025-01-31T09:00:05Z"
    }
  ],
  "invalidated_events": []
}
```

- `entities` and `edges` are what the LLM extracted from this text (after normalization).
- `events` are the temporal events from this text in their stored state; `invalidated_events` lists IDs of older events that this memory closed. Both are always arrays. Their fields are explained in [events.md](events.md).

Errors are returned as `{"error": "..."}`:

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON body, `text` is empty, or `reference_time` is not RFC 3339 |
| 502 | An LLM call failed, or its output could not be parsed or was truncated |
| 503 | LLM not configured (`ANTHROPIC_API_KEY` is not set) |

## How it works

All steps use the LLM from `internal/ai` (Anthropic Messages API, default model `claude-haiku-4-5`, temperature 0). Code is in `internal/memory`.

1. **Extract the graph** (`Extract`, 1 LLM call, max 4096 output tokens): the model is asked to return only JSON of the form:
   ```json
   {
     "entities": [{"name": "...", "type": "...", "description": "..."}],
     "edges": [{"source": "...", "target": "...", "relation": "...", "description": "..."}]
   }
   ```
   Code fences or surrounding prose around the JSON are tolerated.
2. **Extract events** (`ExtractEvents`, 1 LLM call): given the text, the reference time and the entities from step 1, the model lists dated events with their participants.
3. **Resolve events** (`resolveEvents`, 1 LLM call only if needed): new events are compared with stored live events that share a participant. Exact repeats are detected without the LLM; the model flags remaining duplicates and contradictions.
4. **Store** (`Store`): everything is written in a single transaction:
   - the raw text and its reference time go into `gb_memories`;
   - entities are upserted into `gb_entities`;
   - edges are upserted into `gb_edges`, linked to the memory they came from;
   - events are inserted into `gb_events` and linked to their participants in `gb_event_participants`; duplicates are merged and contradicted events are closed (see [events.md](events.md)).

The request makes 2 LLM calls, or 3 when stored events need to be compared. If any step fails, nothing is stored.

### Normalization and deduplication

- Entity `type` is lowercased (empty becomes `other`).
- Edge `relation` is lowercased with spaces replaced by `_`.
- Entities are unique on (lowercased, whitespace-collapsed name, type). Re-submitting an entity updates its description only if the new description is non-empty.
- Edges are unique on (source, target, relation). Re-submitting updates the description (if non-empty) and the source memory.
- An edge that references an entity the LLM did not list creates that entity with type `other`.
- Event participants resolve to an entity from this text, else a stored entity of any type with the same name, else a new entity of type `other`.
- Sending the same text twice adds a second `gb_memories` row but no duplicate entities, edges or events.

## Schema

All tables are prefixed with `gb_` (see `internal/db/db_schema.go`).

| Table | Columns |
| ----- | ------- |
| `gb_memories` | `id`, `text`, `reference_time`, `created_at` |
| `gb_entities` | `id`, `name`, `name_key`, `type`, `description`, `created_at`, `updated_at`; unique (`name_key`, `type`) |
| `gb_edges` | `id`, `source_id` → `gb_entities`, `target_id` → `gb_entities`, `relation`, `description`, `memory_id` → `gb_memories`, `created_at`; unique (`source_id`, `target_id`, `relation`) |
| `gb_events` | `id`, `name`, `description`, `description_key`, `valid_at`, `invalid_at`, `created_at`, `expired_at`, `memory_id` → `gb_memories` |
| `gb_event_participants` | `event_id` → `gb_events`, `entity_id` → `gb_entities`; primary key (`event_id`, `entity_id`) |
| `gb_items` | `id`, `name`, `created_at` (placeholder) |

Foreign keys are enabled on every connection. Deleting an entity cascades to its edges and event participations; deleting an event cascades to its participations; deleting a memory sets `memory_id` to NULL on its edges and events.

Existing database files are not migrated when the schema changes: delete the file and the server recreates it.

## Configuration

- `ANTHROPIC_API_KEY`: required for this endpoint. If missing, the server still starts and the endpoint returns 503.
- `gligabao serve --db <path>` selects the SQLite file (default `bin/gligabao.db`).

## Example

```sh
export ANTHROPIC_API_KEY=...
./run_build.sh
./bin/gligabao serve &
curl -s localhost:8080/v1/gb/remember \
  -d '{"text":"Alice works at Acme and lives in Paris."}'
```

## Limitations

- No authentication.
- Very long input can exceed the LLM's output limit; the request then fails with 502 and nothing is stored.
- `recall` does not use events yet.
