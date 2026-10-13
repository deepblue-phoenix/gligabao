# GET /v1/gb/datasets/{name}/graph

Exports the whole knowledge graph of a [dataset](datasets.md) as JSON: every entity and every edge. Use it to inspect, visualize, back up or migrate what [`remember`](remember.md) has built. It reads the database only and makes no LLM call, so it works even when no LLM is configured.

## Request

```
GET /v1/gb/datasets/main/graph?user_id=u1
```

| Part | Required | Description |
| ---- | -------- | ----------- |
| `{name}` (path) | yes | Dataset name. |
| `user_id` (query) | yes | Owner of the dataset (see [identity.md](identity.md)). |

Only the owner's live dataset can be exported. Datasets of other users, and soft-deleted ones, are not visible.

## Response

`200` with `content-type: application/json`:

```json
{
  "dataset": "main",
  "entities": [
    {"id": 1, "name": "Alice", "type": "person", "description": "Software engineer", "model_id": "5496d6743cdde4fe"},
    {"id": 2, "name": "Acme", "type": "organization", "model_id": "5496d6743cdde4fe"}
  ],
  "edges": [
    {
      "id": 1,
      "source_id": 1,
      "target_id": 2,
      "source": "Alice",
      "target": "Acme",
      "relation": "works_at",
      "description": "Alice works at Acme.",
      "artifact_id": 1,
      "model_id": "5496d6743cdde4fe",
      "valid_at": "2024-01-01T00:00:00Z",
      "created_at": "2025-06-01T10:00:00Z"
    }
  ],
  "models": [
    {"id": "5496d6743cdde4fe", "name": "claude-haiku-5-5", "provider": "anthropic", "kind": "llm", "supported": true, "created_at": "2025-05-01T09:00:00Z"}
  ]
}
```

### Entities (nodes)

| Field | Description |
| ----- | ----------- |
| `id` | Entity ID, unique within the export. Edges refer to it. |
| `name` | Entity name. |
| `type` | Lowercase type (`person`, `organization`, `other`, ...). |
| `description` | Optional; omitted if empty. |
| `model_id` | Optional; the ID of the model that inferred the entity (see `models`): the model of the `extract_graph` stage of the `remember` call, which the caller can pick per call (see [remember.md](remember.md#choosing-the-model-of-each-stage)). For a shared entity it is the model that wrote its stored state: its creator, then whoever last replaced its description. Omitted if unknown. |

### Models

The models that the entities and edges refer to by `model_id`.

| Field | Description |
| ----- | ----------- |
| `id` | Model ID: derived from the name alone (first 8 bytes of the SHA-256 of the lowercased name, hex), so it is the same in every database. |
| `name` | The model name as the provider's API reports it, e.g. `claude-haiku-5-5`. |
| `provider`, `kind` | `anthropic` / `openai`; `llm` / `embedding`. |
| `supported` | True for a model gligabao supports (stored on every startup), false for one only seen in an API response. |

### Edges

| Field | Description |
| ----- | ----------- |
| `id` | Edge ID. |
| `source_id`, `target_id` | IDs of the entities the edge connects (direction: source to target). |
| `source`, `target` | Names of those entities, for convenience. Names are not unique (the same name can have different types), so use the IDs to link nodes. |
| `relation` | Lowercase relation with `_` for spaces, e.g. `works_at`. |
| `description` | Optional; the fact as a sentence. |
| `artifact_id` | Optional; the artifact (the ingested text) the edge came from. |
| `model_id` | Optional; the ID of the model that inferred the fact (see `models`): the model of the `extract_graph` stage of the `remember` call that created it, which the caller can pick per call (see [remember.md](remember.md#choosing-the-model-of-each-stage)). Omitted if unknown. |
| `valid_at`, `invalid_at` | When the fact became true and when it stopped being true (omitted while unknown or still true). |
| `created_at`, `expired_at` | When the fact was learned and when it was found superseded (omitted if not). |

All timestamps are RFC 3339 in UTC. See [temporal.md](temporal.md) for the meaning of the temporal fields.

### Notes

- `entities` and `edges` are ordered by `id`.
- **All** edges are exported, including superseded ones. To get only the facts that are currently true, keep edges without `invalid_at` and `expired_at`.
- Entities with no edges are included.
- The graph of an empty dataset is `{"dataset": "...", "entities": [], "edges": []}`. The lists are never `null`.
- The export is not paginated; it contains the full graph in one response.

## Errors

| Status | Meaning |
| ------ | ------- |
| `400` | `user_id` is missing or invalid. |
| `404` | The user has no live dataset with that name (it never existed, was deleted, belongs to another user, or the name is invalid). |
| `500` | Database error. |

Errors are `{"error": "..."}`.

## Example

```
curl 'http://localhost:7878/v1/gb/datasets/main/graph?user_id=u1' > graph.json
```
