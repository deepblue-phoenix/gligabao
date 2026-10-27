# POST /v1/gb/query

Answers a question from the knowledge graph built by [`remember`](remember.md). The LLM writes a natural-language answer using only graph context, and the response includes `evidence`: the entities and edges it relied on.

## Request

```
POST /v1/gb/query
content-type: application/json

{"user_id": "u1", "query": "Where does Alice work?", "session_id": "chat-42"}
```

- `query` (required): non-empty string. The request body is limited to 64 KiB.
- `user_id` (required): the user who owns the dataset. `400` if missing.
- `dataset` (optional): name of the [dataset](datasets.md) to search. Defaults to `main`. Only that user's dataset graph is used. `404` if it does not exist.
- `session_id` (optional): the user/agent session the call belongs to, a string you choose (at most 128 bytes, no control characters; `400` otherwise). Use the same one as in the `remember` calls of that session, to connect the query to the artifacts (and their entities and facts) of the same session, see [sessions.md](sessions.md). It is echoed in the response and recorded in the call's trajectory and monitoring event. It does **not** change retrieval or the answer.

- `reasoning` (optional): the reasoning (thinking) level of the LLM calls: `none`, `low`, `medium` or `high` (case-insensitive). Either a string, the level of the call that writes the answer, or an object with a level for each stage: `{"query": "medium", "detect_geo": "low"}` (`detect_geo` is the call that finds the places the question mentions, only when the server has [geo](geo.md#in-query) on; ignored otherwise). A stage left out runs with **`none`** (no thinking, so no thinking tokens are paid for). `400` for an unknown level, a stage that is not one of these (the stages of `remember` are rejected too) or a value that is neither a string nor an object. Each call is recorded as an [inference](inferences.md) (`gb_inferences`, type `query`, stage `query` or `detect_geo`) with the level it ran with, its tokens and its cost.

- `geo_nearby` (optional): `true` turns on the search for the [geo entities near the places of the question](geo.md#nearby-entities), which are given to the LLM as context. **Off by default**: the user asks for it. It needs the server to have [geo](geo.md#in-query) on (`400` otherwise). With it, the places of the question that the geo resolver finds are also stored (as entities with coordinates), so that later questions find them in the database.
- `geo_radius_km` (optional): a number of kilometers, the radius of that search. Giving it turns the search on (the same as `geo_nearby: true`; `400` with `geo_nearby: false`). It overrides the server's default (`geo_radius_km` in the config, 50 km unless set). `400` unless it is greater than 0 and at most 20015.
- `models` (optional): an object that picks the LLM model of each stage of the query: `query` (the call that writes the answer) and `detect_geo` (the call that finds the places the question mentions, only when the server has [geo](geo.md#in-query) on; ignored otherwise). A stage left out uses the default model. `400` for another stage (the stages of `remember` are rejected too) or a malformed model name. The model of each call is recorded with its [inference](inferences.md). Its twin `reasoning` takes the same two stages.

## Response

`200 OK`:

```json
{
  "session_id": "chat-42",
  "answer": "Alice works at Acme.",
  "evidence": {
    "entities": [
      {"id": 1, "name": "Alice", "type": "person", "description": "engineer"},
      {"id": 2, "name": "Acme", "type": "organization"}
    ],
    "edges": [
      {"id": 1, "source_id": 1, "target_id": 2, "source": "Alice", "target": "Acme",
       "relation": "works_at", "artifact_id": 1, "valid_at": "2025-01-31T09:00:00Z", "created_at": "2025-01-31T09:00:05Z"}
    ]
  }
}
```

- `session_id`: the session of the request; omitted if it had none.
- `answer`: the LLM's reply, based only on the retrieved graph context. If the graph lacks the information, the answer says so.
- `evidence.entities`: entities used for the answer. `id` is the `gb_entities` row ID.
- `evidence.edges`: edges used for the answer. `id` is the `gb_edges` row ID, `source_id`/`target_id` point at entities, and `artifact_id` (omitted if unset) is the `gb_artifacts` row the edge was inferred from (its artifact). This links each fact back to the original text. `valid_at`, `invalid_at`, `created_at` and `expired_at` (the last two omitted when null) are the fact's bi-temporal fields, see [temporal.md](temporal.md).
- Both evidence lists are always arrays (`[]` when empty, never `null`).

Errors are returned as `{"error": "..."}`:

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON body, `query` is empty, or `session_id` is too long or has control characters |
| 502 | The LLM call failed, or its output could not be parsed, was empty or was truncated |
| 503 | LLM not configured (`ANTHROPIC_API_KEY` is not set) |

## How it works

The code is in `internal/memory/query.go` (logic) and `internal/api/query.go` (HTTP handler).

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

With [geo](geo.md#in-query) on, a second LLM call (stage `detect_geo`) lists the places the question mentions. Only when the request sets `geo_nearby` or `geo_radius_km`, the places, and the entities that matched the question and are places, are used to look up the geo entities within the radius of them, which are added to the context of the answer, and the response has `nearby` (see [Nearby entities](geo.md#nearby-entities)); the places of the question that are resolved are stored too.

### 2. Ask the LLM

The subgraph is rendered as text, with IDs, inside `<graph>` tags:

```
[entity 1] Alice (person): engineer
[entity 2] Acme (organization)
[edge 1] Alice(1) --works_at--> Acme(2): Alice works at Acme. [valid_at=2023-01-01T00:00:00Z, invalid_at=2024-03-01T00:00:00Z, superseded]
```

It is sent with the question and the current time (`<current_time>`) to the LLM (`internal/ai`, default model `claude-haiku-5-5`, max 2048 output tokens). The system prompt tells the model to answer only from the graph, to treat edges with an `invalid_at` as no longer true (use them only for questions about the past), and to return only JSON:

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
- `gligabao serve --db <path>` selects the SQLite file (default `build/gligabao.db`).

## Example

```sh
export ANTHROPIC_API_KEY=...
./run_build.sh
./build/gligabao serve &
curl -s localhost:7878/v1/gb/remember -d '{"user_id":"u1","text":"Alice works at Acme and lives in Paris."}'
curl -s localhost:7878/v1/gb/query   -d '{"user_id":"u1","query":"Where does Alice work?"}'
```

## Limitations

- Retrieval is keyword-based and one hop deep. A question that shares no words with entity names or descriptions (for example a paraphrase or a pronoun-only question) may fall back to recent entities and miss the relevant facts. Multi-hop questions ("who works with people who live in Paris?") only see edges adjacent to the matched entities.
- Edge `relation` and `description` text is not searched during retrieval, only entity names and descriptions.
- Evidence reflects what the LLM *says* it used. IDs are validated, but the model could cite something it did not truly rely on, or omit something it did.
- Only the graph is used; the raw text in `gb_artifacts` is not sent to the LLM.
- No authentication.
