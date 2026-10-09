# POST /v1/gb/recall

Answers a question from the knowledge graph built by [`remember`](remember.md). The LLM writes a natural-language answer using only graph context, and the response includes `evidence`: the entities and edges it relied on.

## Request

```
POST /v1/gb/recall
content-type: application/json

{"query": "Where does Alice work?"}
```

- `query` (required): non-empty string. The request body is limited to 64 KiB.
- `dataset` (optional): name of the [dataset](datasets.md) to search. Defaults to `main`. Only that dataset's graph is used. `404` if it does not exist.

## Response

`200 OK`:

```json
{
  "answer": "Alice works at Acme.",
  "evidence": {
    "entities": [
      {"id": 1, "name": "Alice", "type": "person", "description": "engineer"},
      {"id": 2, "name": "Acme", "type": "organization"}
    ],
    "edges": [
      {"id": 1, "source_id": 1, "target_id": 2, "source": "Alice", "target": "Acme",
       "relation": "works_at", "memory_id": 1, "valid_at": "2025-01-31T09:00:00Z", "created_at": "2025-01-31T09:00:05Z"}
    ]
  }
}
```

- `answer`: the LLM's reply, based only on the retrieved graph context. If the graph lacks the information, the answer says so.
- `evidence.entities`: entities used for the answer. `id` is the `gb_entities` row ID.
- `evidence.edges`: edges used for the answer. `id` is the `gb_edges` row ID, `source_id`/`target_id` point at entities, and `memory_id` (omitted if unset) is the `gb_memories` row the edge came from. This links each fact back to the original text. `valid_at`, `invalid_at`, `created_at` and `expired_at` (the last two omitted when null) are the fact's bi-temporal fields, see [temporal.md](temporal.md).
- Both evidence lists are always arrays (`[]` when empty, never `null`).

Errors are returned as `{"error": "..."}`:

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON body, or `query` is empty |
| 502 | The LLM call failed, or its output could not be parsed, was empty or was truncated |
| 503 | LLM not configured (`ANTHROPIC_API_KEY` is not set) |

## How it works

The code is in `internal/memory/recall.go` (logic) and `internal/api/recall.go` (HTTP handler).

### 1. Retrieve a subgraph

There are no embeddings; retrieval is keyword-based SQL over `gb_entities`.

1. The query is lowercased and split into terms. Terms shorter than 2 characters, common stopwords (`the`, `what`, `where`, ...) and duplicates are dropped. At most 12 terms are kept.
2. Entities that match are scored:
   - +5 if the entity's whole normalized name appears in the query (so `"New York"` matches even though it is two terms);
   - per term, +3 if it appears in the entity name, +1 if it appears in the description.
3. The top **10** entities by score (ties broken by most recently updated) are the *seeds*.
4. If nothing matches, the **25** most recently updated entities are used as seeds instead. This lets vague questions such as "what do you know?" still get an answer.
5. The context is expanded one hop: every edge where a seed is the source or target is added (newest first, capped at **100**), together with the entity at the other end.

If the graph has no entities at all, the endpoint returns a fixed answer ("I don't have any information in memory to answer that.") with empty evidence, without calling the LLM.

### 2. Ask the LLM

The subgraph is rendered as text, with IDs, inside `<graph>` tags:

```
[entity 1] Alice (person): engineer
[entity 2] Acme (organization)
[edge 1] Alice(1) --works_at--> Acme(2): Alice works at Acme. [valid_at=2023-01-01T00:00:00Z, invalid_at=2024-03-01T00:00:00Z, superseded]
```

It is sent with the question and the current time (`<current_time>`) to the LLM (`internal/ai`, default model `claude-haiku-4-5`, temperature 0, max 2048 output tokens). The system prompt tells the model to answer only from the graph, to treat edges with an `invalid_at` as no longer true (use them only for questions about the past), and to return only JSON:

```json
{"answer": "...", "entities": [1, 2], "edges": [1]}
```

`entities` and `edges` are the IDs the model says it used. Code fences or prose around the JSON are tolerated.

### 3. Build the evidence

The cited IDs are validated against the subgraph that was actually sent:

- IDs that were not in the context (hallucinated) are dropped, so evidence only ever contains real rows.
- Duplicate edge IDs are collapsed.
- The source and target entities of every cited edge are added to `evidence.entities`, so each cited edge can be read without looking up its endpoints.
- Results are ordered by ID.

## Configuration

- `ANTHROPIC_API_KEY`: required. If missing, the server still starts and the endpoint returns 503.
- `gligabao serve --db <path>` selects the SQLite file (default `bin/gligabao.db`).

## Example

```sh
export ANTHROPIC_API_KEY=...
./run_build.sh
./bin/gligabao serve &
curl -s localhost:8080/v1/gb/remember -d '{"text":"Alice works at Acme and lives in Paris."}'
curl -s localhost:8080/v1/gb/recall   -d '{"query":"Where does Alice work?"}'
```

## Limitations

- Retrieval is keyword-based and one hop deep. A question that shares no words with entity names or descriptions (for example a paraphrase or a pronoun-only question) may fall back to recent entities and miss the relevant facts. Multi-hop questions ("who works with people who live in Paris?") only see edges adjacent to the matched entities.
- Edge `relation` and `description` text is not searched during retrieval, only entity names and descriptions.
- Evidence reflects what the LLM *says* it used. IDs are validated, but the model could cite something it did not truly rely on, or omit something it did.
- Only the graph is used; the raw text in `gb_memories` is not sent to the LLM.
- No authentication.
