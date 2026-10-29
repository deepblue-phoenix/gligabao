# POST /v1/gb/query

Answers a question from the knowledge graph built by [`remember`](remember.md). The LLM writes a natural-language answer using only the retrieved context (a subgraph, and the passages of long texts close to the question), and the response includes `evidence`: the entities, edges and passages it relied on.

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
    ],
    "passages": []
  },
  "query_run_id": 31
}
```

- `session_id`: the session of the request; omitted if it had none.
- `query_run_id`: the [run log](#run-log) of the call (`gb_query_runs.id`).
- `answer`: the LLM's reply, based only on the retrieved graph context. If the graph lacks the information, the answer says so.
- `evidence.entities`: entities used for the answer. `id` is the `gb_entities` row ID.
- `evidence.edges`: edges used for the answer. `id` is the `gb_edges` row ID, `source_id`/`target_id` point at entities, and `artifact_id` (omitted if unset) is the `gb_artifacts` row the edge was inferred from (its artifact). This links each fact back to the original text. `valid_at`, `invalid_at`, `created_at` and `expired_at` (the last two omitted when null) are the fact's bi-temporal fields, see [temporal.md](temporal.md).
- `evidence.passages`: the passages used for the answer: chunks of a [long text](chunking.md) that were given to the LLM (see [step 2](#2-retrieve-by-similarity-and-passages)). `artifact_id` is the `gb_artifacts` row of the chunk, `parent_id` the row of the whole text it is a chunk of and `index` the position of the chunk in it (from 0); `summary` (omitted if the search did not find it) is the chunk's summary and `text` (omitted for the passages that were given with the summary only) the chunk's text.
- The three evidence lists are always arrays (`[]` when empty, never `null`).

Errors are returned as `{"error": "..."}`:

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON body, `query` is empty, or `session_id` is too long or has control characters |
| 502 | The LLM call failed, or its output could not be parsed, was empty or was truncated; or the question could not be embedded or the vector store could not be searched |
| 503 | LLM not configured (`ANTHROPIC_API_KEY` is not set) |

## How it works

The code is in `internal/memory/memory_query/query.go` (logic) and `internal/api/query/query.go` (HTTP handler). The call is a pipeline of four stages, `queryPipeline` in `query.go`: `retrieve_graph` (steps 1 and 2 below), `detect_geo` and `nearby_geo` (only with [geo](geo.md#in-query)) and `query` (the LLM call of step 3), run by the same runner as the [`remember`](remember.md#how-the-pipeline-is-declared) pipeline. With nothing to answer from, `retrieve_graph` ends the pipeline (`memory_pipeline.Stop`): the answer says so, with no LLM call, and the other stages are `skipped`.

Retrieval has two sources that are added together: keywords (SQL over `gb_entities`) and similarity (the [vector indexes](vectors.md) that `remember` fills). There is no switch for either: both always run, and the embedder and the vector store are required (the server does not start without `OPENAI_API_KEY`).

### 1. Retrieve a subgraph by keywords

1. The query is lowercased and split into terms. Terms shorter than 2 characters, common stopwords (`the`, `what`, `where`, ...) and duplicates are dropped. At most 12 terms are kept.
2. Entities that match are scored:
   - +5 if the entity's whole normalized name appears in the query (so `"New York"` matches even though it is two terms);
   - per term, +3 if it appears in the entity name, +1 if it appears in the description.
3. The top **10** entities by score (ties broken by most recently updated) are the *keyword seeds*.

### 2. Retrieve by similarity and passages

The question is embedded once (one request to the embeddings API, recorded as an [inference](inferences.md) of type `query`, stage `embed`, and as the `search_vectors` step of the trajectory) and searched in the four indexes of the dataset, each match counting from a cosine similarity of **0.75**, the same `remember` asks of two names to be the same entity (a first guess, to be tuned with the evals: a question is not phrased like the text it asks about, so it may be too strict for some of the indexes):

| Index | Taken | What it adds |
| ----- | ----- | ------------ |
| entities | the best 10 | more seeds: the *similarity seeds*, entities the question names in other words (aliases, paraphrases, a description that says what the question asks) |
| edges | the best 10 | the facts themselves, with their entities at both ends, even if no entity of theirs is a seed (this is what searches the text of an edge's `relation` and `description`) |
| chunks, summaries | the best 5 of each | the *passages*, below |

The seeds are the union of the keyword seeds and the similarity seeds (keyword ones first, duplicates dropped); the entities that matched are the *matched* ones of the geo search too. Then:

1. If there is no seed and no similar fact, the **25** most recently updated entities are used as seeds instead. This lets vague questions such as "what do you know?" still get an answer.
2. The context is expanded one hop: every edge where a seed is the source or target is added (newest first, capped at **100**), together with the entity at the other end. The similar facts are added to these (even over the cap), also with their endpoints. Superseded facts are never retrieved, by keywords or by similarity, although their vectors are still in the index (see [Facts over time](#facts-over-time-and-history)).
3. **Passages.** Each match of the chunk index is a chunk of a [long text](chunking.md), each match of the summary index is the summary of one: they are put together by chunk (a chunk's score is the better of its two) and ranked. The best **3** are given with the text of the chunk (the costly part of the context, up to a chunk each) and, if the summary was found too, its summary. Up to **5** more are given with their summary only (a chunk found only by its own text is left out then). Texts that were not chunked have no passage: their entities and facts are in the graph.

Every hit is resolved through the database: a vector whose row was deleted is dropped, and nothing is read from the vector's metadata.

If there is nothing to answer from (no entity or fact in the context, and no passage), the endpoint returns a fixed answer ("I don't have any information in memory to answer that.") with empty evidence, without calling the LLM.

With [geo](geo.md#in-query) on, a second LLM call (stage `detect_geo`) lists the places the question mentions. Only when the request sets `geo_nearby` or `geo_radius_km`, the places, and the entities that matched the question and are places, are used to look up the geo entities within the radius of them, which are added to the context of the answer, and the response has `nearby` (see [Nearby entities](geo.md#nearby-entities)); the places of the question that are resolved are stored too.

### 3. Ask the LLM

The subgraph is rendered as text, with IDs, inside `<graph>` tags:

```
[entity 1] Alice (person): engineer
[entity 2] Acme (organization)
[edge 1] Alice(1) --works_at--> Acme(2): Alice works at Acme. [valid_at=2023-01-01T00:00:00Z, invalid_at=2024-03-01T00:00:00Z]
```

The passages follow, inside `<passages>` tags (the section is left out if there are none):

```
[passage 14] chunk 3 of text 12
summary: Alice joined Acme in 2023 and ...
text: ...the chunk...
```

It is sent with the question and the current time (`<current_time>`) to the LLM (`internal/ai`, default model `claude-haiku-5-5`, max 2048 output tokens). The system prompt tells the model to answer only from the graph and the passages, to treat edges with an `invalid_at` as no longer true (use them only for questions about the past), and to return only JSON:

```json
{"answer": "...", "entities": [1, 2], "edges": [1], "passages": [14]}
```

`entities`, `edges` and `passages` are the IDs the model says it used. Code fences or prose around the JSON are tolerated.

### 4. Build the evidence

The cited IDs are validated against the subgraph that was actually sent:

- IDs that were not in the context (hallucinated) are dropped, so evidence only ever contains real rows.
- Duplicate edge IDs are collapsed.
- A cited passage is the chunk that was in the context, with its summary and text as they were given.
- The source and target entities of every cited edge are added to `evidence.entities`, so each cited edge can be read without looking up its endpoints.
- Results are ordered by ID.

### Run log

Every call is recorded in `gb_query_runs`: **one row per execution of the pipeline, the authoritative record of how it was executed**, the same kind of record as the [run log of `remember`](remember.md#run-log) (`internal/memory/memory_query/runlog.go`). The row is written when the call starts and updated at every step, and a row that stays `started` belongs to a process that died. A request that is rejected before the pipeline starts (a `400`, an unknown dataset) has no row.

| Column | |
| ------ | - |
| `id`, `user_id`, `dataset` (the name), `question` | the call |
| `params` | JSON: how it was made (models and reasoning per stage, the options of the geo search); `session_id` is a column of its own (`''` if there was none) |
| `status`, `error` | **the call as a whole**: `started`, then `succeeded` if `query` returned an answer or `failed` (with `error`) if it returned an error |
| `started_at`, `finished_at` | of the call |

and, for each of the stages `retrieve_graph`, `detect_geo`, `nearby_geo`, `query` (`relational.QueryStages`), the columns `<stage>_status`, `<stage>_error`, `<stage>_started_at` and `<stage>_finished_at`, and for the stages that run a model (`retrieve_graph`, which embeds the question, `detect_geo` and `query`) `<stage>_inference_ids`: the `gb_inferences` IDs of what the stage ran. The statuses are those of `remember`: `not_started`, `started`, `succeeded`, `failed` and `skipped` (the call does not use the stage: no geo resolver, no proximity search, or the pipeline ended before it). `detect_geo` and `nearby_geo` never fail the call, so they can be `failed` in a run that is `succeeded`.

From Go: `relational.GetQueryRun`, `ListQueryRuns` (filter by user, dataset and status). The admin API serves them at `GET /v1/gb/admin/api/query-runs` (the counts by status, and the filtered, paged list; like `remember-runs`).

Existing database files are not migrated: a database created before 0.124.0 gets `gb_query_runs` when the server starts, but its `gb_remember_runs` does not accept the status `skipped`, so delete the file and the server recreates it.

### Facts over time and history

Facts are [bi-temporal](temporal.md): `valid_at` / `invalid_at` say when a fact was true in the world, and `expired_at` says when newer knowledge superseded it. `query` treats the two differently:

| The edge | In the context? | What the LLM sees |
| -------- | --------------- | ----------------- |
| live: no `invalid_at`, no `expired_at` | yes | `valid_at` only: still true |
| ended without being superseded: has an `invalid_at`, no `expired_at` (the text said it ended) | yes | `valid_at` and `invalid_at` |
| superseded: has an `expired_at` (a later fact replaced or contradicted it; `remember` also closes it with an `invalid_at`) | **no** | nothing |

- **Retrieved by anything.** The filter is on the edge itself (`expired_at IS NULL`, in `graph.EdgesAround` and `graph.EdgesByID`), so it holds for every way a fact can get into the context: the edges around the keyword and similarity seeds, the facts found in the edge index, and the edges around the entities of the [nearby](geo.md#nearby-entities) search. A superseded fact's vector stays in the edge index (`remember` does not delete it), so a similarity match on it is simply dropped when it is resolved through the database. The filter is applied in the query itself, so the cap of 100 edges counts live facts only.
- **Present and past.** The prompt gives the LLM the current time and tells it that an edge without `invalid_at` is still true and one with it stopped being true at that time. So "Where does Alice work now?" is answered from the live edge, and a question about the past can use an edge that ended without being superseded (it is not marked in any other way: the LLM reads `valid_at` and `invalid_at`).
- **What is not answerable.** Once a fact is superseded, `query` cannot answer questions about what used to be true from it: if Alice moved from Acme to Globex and `remember` closed the Acme edge because of the Globex one, "Where did Alice work before?" has no Acme edge to use (the text the fact came from, if it was chunked, may still come back as a [passage](#2-retrieve-by-similarity-and-passages)). The superseded edges are kept in the database with their validity, for [temporal.md](temporal.md) and the admin views; they are just not retrieved.
- **Evidence** edges carry the temporal fields as they are stored (`valid_at`, `invalid_at`, `created_at`, and `expired_at`, which is always null here since only live facts are cited).

## Configuration

- `ANTHROPIC_API_KEY`: required. If missing, the server still starts and the endpoint returns 503.
- `OPENAI_API_KEY` (or `openai_api_key`): required, as for `remember`: it embeds the question. The server does not start without it.
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

- Retrieval is one hop deep: multi-hop questions ("who works with people who live in Paris?") only see edges adjacent to the seeds (and the similar facts). A pronoun-only question ("what about her?") still has nothing to match, by keywords or by similarity, and falls back to recent entities.
- The similarity thresholds and the numbers of matches taken are a first guess; they have not been tuned with the evals yet. The seeds are a plain union, not ranked together.
- The passages do not lead to the entities and facts extracted from them (the provenance links `gb_entity_artifacts` and `gb_edge_artifacts` are not followed), and a text that was not chunked has none.
- The question is embedded on its own: the conversation (`session_id`) does not take part in it.
- Superseded facts are not retrieved, so history that was replaced by newer knowledge cannot be answered from the graph (see [Facts over time](#facts-over-time-and-history)).
- Evidence reflects what the LLM *says* it used. IDs are validated, but the model could cite something it did not truly rely on, or omit something it did.
- Only the text of the best few chunks is sent to the LLM (see [step 2](#2-retrieve-by-similarity-and-passages)); the rest of the raw text in `gb_artifacts` is not.
- No authentication.
