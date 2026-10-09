# POST /v1/gb/remember

Takes a block of text, stores it as an episode, uses an LLM to extract a small knowledge graph (entities and edges) whose edges are bi-temporal facts, and stores everything in SQLite. The temporal model is described in [temporal.md](temporal.md).

## Request

```
POST /v1/gb/remember
content-type: application/json

{"user_id": "u1", "text": "Alice works at Acme and lives in Paris.", "reference_time": "2025-01-31T09:00:00Z"}
```

- `user_id` (required): the user who owns the dataset. `400` if missing.
- `text` (required): non-empty string. The request body is limited to 1 MiB.
- `dataset` (optional): name of the [dataset](datasets.md) to store into; created if it does not exist. Defaults to `main`. Entities and edges are deduplicated and reconciled only within the dataset.
- `reference_time` (optional): RFC 3339 timestamp of when the text was written or observed. Relative dates in the text ("yesterday", "last March") are resolved against it. Defaults to the current time.

## Response

`200 OK`:

```json
{
  "user_id": "u1",
  "dataset": "main",
  "memory_id": 1,
  "valid_at": "2025-01-31T09:00:00Z",
  "created_at": "2025-01-31T09:00:05Z",
  "entities": [
    {"name": "Alice", "type": "person", "description": "engineer"},
    {"name": "Acme", "type": "organization"},
    {"name": "Paris", "type": "other"}
  ],
  "edges": [
    {"id": 1, "source": "Alice", "target": "Acme", "relation": "works_at", "description": "Alice works at Acme.",
     "valid_at": "2025-01-31T09:00:00Z", "created_at": "2025-01-31T09:00:05Z"},
    {"id": 2, "source": "Alice", "target": "Paris", "relation": "lives_in", "description": "Alice lives in Paris.",
     "valid_at": "2025-01-31T09:00:00Z", "created_at": "2025-01-31T09:00:05Z"}
  ],
  "invalidated_edges": []
}
```

- `memory_id`, `valid_at`, `created_at`: the episode and its event time (the `reference_time`) and ingestion time.
- `entities` are what the LLM extracted from this text (after normalization).
- `edges` are the facts from this text in their stored state; if a new edge repeated a stored one, the stored edge is returned. Edge fields `invalid_at`, `expired_at` are omitted when null.
- `invalidated_edges` lists IDs of edges closed because of this memory (see [temporal.md](temporal.md)). Always an array.

Errors are returned as `{"error": "..."}`:

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON body, `text` is empty, or `reference_time` is not RFC 3339 |
| 502 | An LLM call failed, or its output could not be parsed or was truncated |
| 503 | LLM not configured (`ANTHROPIC_API_KEY` is not set) |

## How it works

All steps use the LLM from `internal/ai` (Anthropic Messages API, default model `claude-haiku-4-5`, temperature 0). Code is in `internal/memory`.

1. **Extract the graph** (`Extract`, 1 LLM call, max 4096 output tokens): given the reference time and the text, the model is asked to return only JSON of the form:
   ```json
   {
     "entities": [{"name": "...", "type": "...", "description": "..."}],
     "edges": [{"source": "...", "target": "...", "relation": "...", "description": "...", "valid_at": "...", "invalid_at": "..."}]
   }
   ```
   Code fences or surrounding prose around the JSON are tolerated. An edge without `valid_at` gets the reference time.
2. **Resolve edges** (`resolveEdges`, 1 LLM call only if needed): new edges are compared with stored live edges that touch the same entities. Exact repeats are detected without the LLM; the model flags remaining duplicates and contradictions.
3. **Store** (`Store`): everything is written in a single transaction:
   - the raw text and its reference time go into `gb_memories` (the episode);
   - entities are upserted into `gb_entities`;
   - edges are inserted into `gb_edges`, linked to the episode they came from; duplicates are merged and contradicted edges are closed (see [temporal.md](temporal.md)).

The request makes 1 LLM call, or 2 when stored edges need to be compared. If any step fails, nothing is stored.

### Normalization and deduplication

- Entity `type` is lowercased (empty becomes `other`).
- Edge `relation` is lowercased with spaces replaced by `_`.
- Entities are unique on (lowercased, whitespace-collapsed name, type). Re-submitting an entity updates its description only if the new description is non-empty.
- Edge endpoints resolve to an entity from this text, else a stored entity of any type with the same name, else a new entity of type `other`.
- A new edge with the same source, target and relation as a stored edge that is still true is a duplicate and is not inserted again.
- Sending the same text twice adds a second `gb_memories` row but no duplicate entities or edges.

## Schema

All tables are prefixed with `gb_` (see `internal/db/db_schema.go`).

| Table | Columns |
| ----- | ------- |
| `gb_datasets` | `id`, `name`, `description`, `tags` (JSON), `created_at`, `deleted`, `deleted_at`; unique `name` among non-deleted |
| `gb_memories` | `id`, `dataset_id` → `gb_datasets`, `text`, `valid_at`, `created_at` |
| `gb_entities` | `id`, `dataset_id` → `gb_datasets`, `name`, `name_key`, `type`, `description`, `created_at`, `updated_at`; unique (`dataset_id`, `name_key`, `type`) |
| `gb_edges` | `id`, `dataset_id` → `gb_datasets`, `source_id` → `gb_entities`, `target_id` → `gb_entities`, `relation`, `description`, `valid_at`, `invalid_at`, `created_at`, `expired_at`, `memory_id` → `gb_memories` |
| `gb_items` | `id`, `name`, `created_at` (placeholder) |

Foreign keys are enabled on every connection. Deleting an entity cascades to its edges; deleting a memory sets `memory_id` to NULL on its edges.

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
  -d '{"user_id":"u1","text":"Alice works at Acme and lives in Paris."}'
```

## Limitations

- No authentication.
- Very long input can exceed the LLM's output limit; the request then fails with 502 and nothing is stored.
