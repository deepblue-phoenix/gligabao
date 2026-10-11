# POST /v1/gb/remember

Takes a block of text, stores it as an artifact (an episode), uses an LLM to extract a small knowledge graph (entities and edges) whose edges are bi-temporal facts, and stores everything in SQLite. The temporal model is described in [temporal.md](temporal.md).

## Request

```
POST /v1/gb/remember
content-type: application/json

{"user_id": "u1", "text": "Alice works at Acme and lives in Paris.", "kind": "episode", "reference_time": "2025-01-31T09:00:00Z"}
```

- `user_id` (required): the user who owns the dataset. `400` if missing.
- `text` (required): non-empty string. The request body is limited to 1 MiB.
- `dataset` (optional): name of the [dataset](datasets.md) to store into; created if it does not exist. Defaults to `main`. Entities and edges are deduplicated and reconciled only within the dataset.
- `kind` (optional): the artifact kind, `knowledge` or `episode` (case-insensitive). Defaults to `knowledge`: a text that is not strictly episodic (a reference text, a note, ...). Use `episode` for a Graphiti-style episode, a text anchored in time (a message, a news item). Any other value returns `400`. The kind is stored on the artifact (`gb_artifacts.kind`) and echoed in the response; it does not change how the text is extracted or reconciled yet.
- `reference_time` (optional): RFC 3339 timestamp of when the text was written or observed. Relative dates in the text ("yesterday", "last March") are resolved against it. Defaults to the current time.

## Response

`200 OK`:

```json
{
  "user_id": "u1",
  "dataset": "main",
  "kind": "episode",
  "artifact_id": 1,
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

- `artifact_id`, `kind`, `valid_at`, `created_at`: the artifact, its kind, and its event time (the `reference_time`) and ingestion time.
- `entities` are what the LLM extracted from this text (after normalization).
- `edges` are the facts from this text in their stored state; if a new edge repeated a stored one, the stored edge is returned. Edge fields `invalid_at`, `expired_at` are omitted when null.
- `invalidated_edges` lists IDs of edges closed because of this artifact (see [temporal.md](temporal.md)). Always an array.

Errors are returned as `{"error": "..."}`:

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON body, `text` is empty, `kind` is not `knowledge` or `episode`, or `reference_time` is not RFC 3339 |
| 502 | An LLM call failed, or its output could not be parsed or was truncated |
| 503 | LLM not configured (`ANTHROPIC_API_KEY` is not set) |

## How it works

All steps use the LLM from `internal/ai` (Anthropic Messages API, default model `claude-haiku-5-5`). Code is in `internal/memory`.

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
   - the raw text and its reference time go into `gb_artifacts` (with its `kind`);
   - entities are upserted into `gb_entities` and linked to the artifact in `gb_entity_artifacts`, including stored entities that an edge names but the extraction did not list;
   - edges are inserted into `gb_edges`, anchored to the artifact that created them (`artifact_id`) and linked to it in `gb_edge_artifacts`; a duplicate of a stored edge is not inserted but the new artifact is added to that edge's `gb_edge_artifacts` links; duplicates are merged and contradicted edges are closed (see [temporal.md](temporal.md)).

The request makes 1 LLM call, or 2 when stored edges need to be compared. If any step fails, nothing is stored.

### Normalization and deduplication

- Entity `type` is lowercased (empty becomes `other`).
- Edge `relation` is lowercased with spaces replaced by `_`.
- Entities are unique on (lowercased, whitespace-collapsed name, type). Re-submitting an entity updates its description only if the new description is non-empty.
- Edge endpoints resolve to an entity from this text, else a stored entity of any type with the same name, else a new entity of type `other`.
- A new edge with the same source, target and relation as a stored edge that is still true is a duplicate and is not inserted again.
- Sending the same text twice adds a second `gb_artifacts` row (linking the existing entities and edges to it) but no duplicate entities or edges.

## Schema

All tables are prefixed with `gb_` (see `internal/db/relational/{sqlite,postgres}/db_schema.go` for datasets and `internal/db/graph/{sqlite,postgres}/db_schema.go` for the graph tables).

| Table | Columns |
| ----- | ------- |
| `gb_datasets` | `id`, `name`, `description`, `tags` (JSON), `created_at`, `deleted`, `deleted_at`; unique `name` among non-deleted |
| `gb_artifacts` | `id`, `dataset_id` → `gb_datasets`, `kind` (`knowledge` (default) or `episode`), `text`, `valid_at`, `created_at` |
| `gb_entities` | `id`, `dataset_id` → `gb_datasets`, `name`, `name_key`, `type`, `description`, `created_at`, `updated_at`; unique (`dataset_id`, `name_key`, `type`) |
| `gb_edges` | `id`, `dataset_id` → `gb_datasets`, `source_id` → `gb_entities`, `target_id` → `gb_entities`, `relation`, `description`, `valid_at`, `invalid_at`, `created_at`, `expired_at`, `artifact_id` → `gb_artifacts` (the creating artifact) |
| `gb_edge_artifacts` | `edge_id` → `gb_edges`, `artifact_id` → `gb_artifacts`; primary key (`edge_id`, `artifact_id`) |
| `gb_entity_artifacts` | `entity_id` → `gb_entities`, `artifact_id` → `gb_artifacts`; primary key (`entity_id`, `artifact_id`) |
| `gb_items` | `id`, `name`, `created_at` (placeholder) |

Foreign keys are enabled on every connection. Deleting an entity cascades to its edges and its `gb_entity_artifacts` links, and deleting an edge removes its `gb_edge_artifacts` links; deleting an artifact sets `artifact_id` to NULL on its edges and removes its `gb_edge_artifacts` and `gb_entity_artifacts` links.

**Why edges have a link table too.** `gb_edges.artifact_id` is only the artifact that created the edge. When a later artifact states the same fact, the new edge is recognized as a duplicate (exactly, or by the LLM) and merged into the stored one instead of being inserted, so without a link table the fact that this artifact also supports it would be lost. `gb_edge_artifacts` holds the creating artifact and every artifact whose duplicate was merged in, so a fact's full provenance is kept (how many sources back it, which texts to cite). Contradicted edges are not linked: a contradiction is not a restatement.

**Why entities have a link table.** An edge is created by exactly one artifact, so it carries a plain `artifact_id`. An entity is not: entities are deduplicated per dataset on (`name_key`, `type`), so "Alice" mentioned in artifacts 1, 5 and 9 is one row, and one `artifact_id` column could only remember where she first appeared. The many-to-many `gb_entity_artifacts` table records every artifact an entity was inferred from, so you can ask which artifacts ground an entity and which entities an artifact contributes to, and an artifact can be removed without losing entities that other artifacts still support.

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
