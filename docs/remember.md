# POST /v1/gb/remember

Takes a block of text, uses an LLM to extract entities and edges (a small knowledge graph), and stores them in SQLite.

## Request

```
POST /v1/gb/remember
content-type: application/json

{"text": "Alice works at Acme and lives in Paris."}
```

- `text` (required): non-empty string. The request body is limited to 1 MiB.

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
  ]
}
```

Errors are returned as `{"error": "..."}`:

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON body, or `text` is empty |
| 502 | The LLM call failed, or its output could not be parsed or was truncated |
| 503 | LLM not configured (`ANTHROPIC_API_KEY` is not set) |

## How it works

1. **Extract** (`internal/memory`): the text is sent to the LLM (`internal/ai`, Anthropic Messages API, default model `claude-haiku-4-5`, temperature 0, max 4096 output tokens). The model is asked to return only JSON of the form:
   ```json
   {
     "entities": [{"name": "...", "type": "...", "description": "..."}],
     "edges": [{"source": "...", "target": "...", "relation": "...", "description": "..."}]
   }
   ```
   Code fences or surrounding prose around the JSON are tolerated.
2. **Store**: everything is written in a single transaction:
   - the raw text goes into `gb_memories`;
   - entities are upserted into `gb_entities`;
   - edges are upserted into `gb_edges`, linked to the memory they came from.

### Normalization and deduplication

- Entity `type` is lowercased (empty becomes `other`).
- Edge `relation` is lowercased with spaces replaced by `_`.
- Entities are unique on (lowercased, whitespace-collapsed name, type). Re-submitting an entity updates its description only if the new description is non-empty.
- Edges are unique on (source, target, relation). Re-submitting updates the description (if non-empty) and the source memory.
- An edge that references an entity the LLM did not list creates that entity with type `other`.
- Sending the same text twice adds a second `gb_memories` row but no duplicate entities or edges.

## Schema

All tables are prefixed with `gb_` (see `internal/db/db_schema.go`).

| Table | Columns |
| ----- | ------- |
| `gb_memories` | `id`, `text`, `created_at` |
| `gb_entities` | `id`, `name`, `name_key`, `type`, `description`, `created_at`, `updated_at`; unique (`name_key`, `type`) |
| `gb_edges` | `id`, `source_id` → `gb_entities`, `target_id` → `gb_entities`, `relation`, `description`, `memory_id` → `gb_memories`, `created_at`; unique (`source_id`, `target_id`, `relation`) |
| `gb_items` | `id`, `name`, `created_at` (placeholder) |

Foreign keys are enabled on every connection. Deleting an entity cascades to its edges; deleting a memory sets `memory_id` to NULL on its edges.

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
