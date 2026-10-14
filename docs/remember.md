# POST /v1/gb/remember

Takes a block of text (or, with `image`, an image: see [images.md](images.md)), stores it as an [artifact](artifacts.md), uses an LLM to extract a small knowledge graph (entities and edges) whose edges are bi-temporal facts, stores everything in the database, and embeds the entities and edges into the dataset's [vector datasets](vectors.md#datasets). A text of more than one chunk (1024 tokens by default) is a document: it is [chunked](chunking.md), and each chunk is extracted, stored and embedded on its own. The temporal model is described in [temporal.md](temporal.md).

## Request

```
POST /v1/gb/remember
content-type: application/json

{"user_id": "u1", "text": "Alice works at Acme and lives in Paris.", "kind": "episode", "reference_time": "2025-01-31T09:00:00Z", "session_id": "chat-42"}
```

- `user_id` (required): the user who owns the dataset. `400` if missing.
- `text` (required unless `image` is given): non-empty string, at most 1 MiB. If it is larger than the chunk size (`chunk_size`, 1024 tokens by default) it is split into overlapping chunks: see [chunking.md](chunking.md).
- `image` (optional): a base64-encoded image, instead of `text`. The request then goes through the separate [image pipeline](images.md) (OCR of the title and text blocks, and a theme such as `plot` or `graph`), with a different response; `text` and `kind` must not be given with it.
- `dataset` (optional): name of the [dataset](datasets.md) to store into; created if it does not exist. Defaults to `main`. Entities and edges are deduplicated and reconciled only within the dataset.
- `kind` (optional): the artifact kind, `knowledge` or `episode` (case-insensitive). Defaults to `knowledge`: a text that is not strictly episodic (a reference text, a note, ...). Use `episode` for a Graphiti-style episode, a text anchored in time (a message, a news item). Any other value returns `400`. The kind is stored on the artifact (`gb_artifacts.kind`) and echoed in the response; it does not change how the text is extracted or reconciled yet.
- `reference_time` (optional): RFC 3339 timestamp of when the text was written or observed. Relative dates in the text ("yesterday", "last March") are resolved against it. Defaults to the current time.
- `session_id` (optional): the user/agent session the call belongs to, a string you choose (at most 128 bytes, no control characters; `400` otherwise). It is stored on the artifact (`gb_artifacts.session_id`) and recorded against every entity and edge the artifact yields, in `gb_entity_sessions` and `gb_edge_sessions`, **including entities and facts that already existed** and so were not written again. See [sessions.md](sessions.md). Also accepted with `image`.

- `timeseries` (optional): timeseries to store and anchor to what the text yields, `{"series": [{"key": "AAPL.close", "points": [["2025-01-31T00:00:00Z", 222.0]], "anchors": [{"entity": {"name": "Apple"}}]}]}`. A series without anchors is anchored to the artifact; entity and edge anchors are looked up after the text is extracted, so they may name entities that the text will yield. `400` if malformed (before any LLM call) or together with `image`, `422` if an anchor that must match finds nothing (nothing is stored). See [timeseries.md](timeseries.md). `timeseries.fetch` (`{"providers": ["prices"], "before": "30d", "after": "0"}`, every field optional; alone or next to `series`) makes the server fetch series from the timeseries providers it was started with, for the entities of the text (`503` if it has none): see [timeseries.md](timeseries.md#providers). `timeseries.observations` (`true`; with `series` or `fetch`) makes `remember` also derive a `series_observation` artifact from each series it stored, extracted into the graph like a text: see [timeseries.md](timeseries.md#observations).

- `models` (optional): an object that picks the LLM model of each LLM-invoking stage of the pipeline; see [Choosing the model of each stage](#choosing-the-model-of-each-stage). `400` for an unknown stage or a malformed model name.

- `web_search` (optional, default `false`): after the text is stored, search the web (with Anthropic's cloud-based web search tool) for the latest world events about its key entities, and remember what is found: each result becomes an artifact of its own, of kind `web_search`, which saves the query that was issued and hangs off the artifact the entity came from, and the entities and facts in it join the graph like any other. Only available with an Anthropic API key (`503` otherwise); `400` with `image`. See [web-search.md](web-search.md).

## Response

`200 OK`:

```json
{
  "user_id": "u1",
  "dataset": "main",
  "kind": "episode",
  "artifact_id": 1,
  "session_id": "chat-42",
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
  "invalidated_edges": [],
  "embedded_entities": 3,
  "embedded_edges": 2
}
```

- `artifact_id`, `kind`, `valid_at`, `created_at`: the artifact, its kind, and its event time (the `reference_time`) and ingestion time.
- `session_id`: the session the artifact was attached to; omitted if the request had none.
- `entities` are what the LLM extracted from this text (after normalization).
- `edges` are the facts from this text in their stored state; if a new edge repeated a stored one, the stored edge is returned. Edge fields `invalid_at`, `expired_at` are omitted when null.
- `invalidated_edges` lists IDs of edges closed because of this artifact (see [temporal.md](temporal.md)). Always an array.
- `series`: only with `timeseries`: one item per series (`id`, `key`, `created`, `points`, `point_count`, `stats`, `anchors`, ...). See [timeseries.md](timeseries.md#with-remember).
- `series_fetches`: only with `timeseries.fetch`: one item per (entity, provider) the LLM chose (`entity`, `provider`, `query`, `reason`, `from`, `to`, the `series` stored, and an `error` if the provider failed). See [timeseries.md](timeseries.md#providers).
- `series_observations`: only with `timeseries.observations`: one item per series observed (`series_id`, `key`, `parent_artifact_id`, `query`, `from`, `to`, the generated `text`, the `artifact_id` stored, its `entities` and `edges`, and an `error` if it failed). See [timeseries.md](timeseries.md#observations).
- `web_searches`: only with `web_search`: one item per key entity that was searched for (`entity`, `query`, `parent_artifact_id`, the `web_search` `artifact_id`, the `sources`, and the `entities` and `edges` the result yielded; `entities` and `edges` above stay those of the text). See [web-search.md](web-search.md#response).
- `chunks`, `embedded_chunks`: only for a text that was [chunked](chunking.md). `artifact_id` is then the parent artifact (the whole text), `chunks` lists the chunk artifacts (`artifact_id`, `index`, `tokens`, `overlap_tokens`, `start_offset`, `end_offset`, `entities`, `edges`), `entities`, `edges` and `invalidated_edges` are those of all the chunks, and `embedded_chunks` counts the chunk vectors written.
- `embedded_entities` and `embedded_edges` are the number of vectors written to the dataset's entity and edge indexes (see [Embed](#how-it-works)). Both are omitted when nothing was embedded (no `OPENAI_API_KEY`). `embedded_entities` counts the entities the text mentions, including those that already existed, and `embedded_edges` the facts it states, including repeated ones; they can be more than the number of new rows.

Errors are returned as `{"error": "..."}`:

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON body, `text` is empty, `timeseries` is malformed or given with an `image`, `kind` is not `knowledge` or `episode`, `reference_time` is not RFC 3339, `session_id` is too long or has control characters, `models` names an unknown stage or has a malformed model name, or `web_search` is given with an `image` |
| 422 | A series anchor with `on_unmatched: "error"`, or an ID, matched nothing; nothing is stored (for a chunked text, only the series; see [timeseries.md](timeseries.md#with-remember)) |
| 502 | An LLM call failed (this includes a `models` entry the provider does not know), or its output could not be parsed or was truncated; or embedding failed (nothing stored); or the vectors could not be written to the vector store (after the database was saved, see [Embed](#how-it-works)) |
| 503 | LLM not configured (`ANTHROPIC_API_KEY` is not set), or `web_search` was asked for and web search is not available (it needs that key too), or `timeseries.fetch` was asked for and the server has no timeseries provider |

## How it works

This is the pipeline for a text that fits one chunk. A longer text runs the same steps on each of its chunks, one after the other; see [chunking.md](chunking.md).

All steps use the LLM from `internal/ai` (Anthropic Messages API, default model `claude-haiku-5-5`, unless the request picks another one for a stage: see [Choosing the model of each stage](#choosing-the-model-of-each-stage)). Code is in `internal/memory`.

0. **Classify complexity** (`classifyComplexity`, 1 call to Jev, only if a Jev API key is configured: `jev_api_key` / `JEV_API_KEY`): Jev, TypeSafe's system 1 model (package `internal/jev`, `POST /v1/systemone`), answers one `choice` question: is the text `simple` (up to 5 entities), `medium` (6 to 20) or `complex` (more than 20) in the number of entities it will yield? This predicts how much compute the later steps need per entity and edge. The answer's `confidence` (0 to 1) and the class probabilities are kept. For now the result is only logged (`jev: remember text complexity`: `complexity`, `confidence`, `latency_ms`). It is advisory (a failure is logged and the request goes on), it always runs on Jev (it is not in `models`), and it sees at most the first 32 KiB of the text. In an eval [trajectory](evals.md) it is the `classify_complexity` step, whose extra has `latency_ms` and `started_at`.
1. **Extract the graph** (`Extract`, 1 LLM call, max 4096 output tokens): given the reference time and the text, the model is asked to return only JSON of the form:
   ```json
   {
     "entities": [{"name": "...", "type": "...", "description": "..."}],
     "edges": [{"source": "...", "target": "...", "relation": "...", "description": "...", "valid_at": "...", "invalid_at": "..."}]
   }
   ```
   Code fences or surrounding prose around the JSON are tolerated. An edge without `valid_at` gets the reference time.
2. **Resolve edges** (`resolveEdges`, 1 LLM call only if needed): new edges are compared with the [candidate facts](#candidate-facts-load_candidates): stored live edges that touch the same entities. Exact repeats are detected without the LLM; the model flags remaining duplicates and contradictions.
3. **Store** (`Store`): everything is written in a single transaction:
   - the raw text and its reference time go into `gb_artifacts` (with its `kind` and `session_id`);
   - entities are upserted into `gb_entities` and linked to the artifact in `gb_entity_artifacts`, including stored entities that an edge names but the extraction did not list;
   - edges are inserted into `gb_edges`, anchored to the artifact that created them (`artifact_id`) and linked to it in `gb_edge_artifacts`; a duplicate of a stored edge is not inserted but the new artifact is added to that edge's `gb_edge_artifacts` links; duplicates are merged and contradicted edges are closed (see [temporal.md](temporal.md));
   - with a `session_id`, every entity linked to the artifact is also linked to the session in `gb_entity_sessions`, and every edge linked to it in `gb_edge_sessions` (see [sessions.md](sessions.md)). Both happen for duplicates too, so the sessions an entity or fact was discovered in are all kept.

4. **Embed** (`WithVectors`, 1 embeddings request, only if an OpenAI key is configured): the entities this text mentions and the edges it states (and, if the text is a chunk of a longer one, the chunk's own text, into a third vector dataset: see [chunking.md](chunking.md#chunks-are-embedded)) are embedded together with `text-embedding-3-small`: an entity as `Name (type): description`, an edge as its description. This happens inside step 3, after the rows are written and before the transaction commits, so a failure stores nothing. Right after the commit the vectors are written, with `Upsert`, to the dataset's two vector datasets: `dataset-<id>-entities` (vector `entity-<entity id>`) and `dataset-<id>-edges` (vector `edge-<edge id>`). Vectors are keyed by ID, so remembering an entity or fact again overwrites its vector. Details, metadata and failure handling: [vectors.md](vectors.md#what-remember-writes).

4b. **Timeseries** (only with `timeseries`): the supplied series are written inside step 3's transaction, after the entities and edges, and anchored: to the artifact, and to the entities and edges that their selectors find. A series that cannot be anchored stores nothing. For a chunked text they are stored after the last chunk, anchored to the parent. See [timeseries.md](timeseries.md).

5. **Web search** (only with `web_search`, and only with an Anthropic API key): the key entities of the text are searched for on the web and the results are remembered as `web_search` artifacts, each with its query and its parent (the artifact the entity came from), and their own entities and edges. It runs after the text was stored, so a failure in it never fails the request. See [web-search.md](web-search.md).

6. **Timeseries fetch** (only with `timeseries.fetch`, and only when the server has [timeseries providers](timeseries.md#providers)): an LLM decides from the providers' descriptions which provider fetches series for which entity of the text (only the entities of the text itself, not those a web search found), the providers' callbacks run, and the series are stored and anchored to the entity and the artifact it came from. It runs after the text was stored, so a failure in it never fails the request.

7. **Series observations** (only with `timeseries.observations`, and only for series that were stored in this request): a text is generated in Go from the stats of each series (at most 10) over the period the request wrote, extracted with the `extract_graph` model, reconciled and stored as a `series_observation` artifact, a child of the artifact that anchors the series. It runs last, after the text was stored, so a failure in it never fails the request. See [timeseries.md](timeseries.md#observations).

Steps 1 and 2 each run on their [own model](#choosing-the-model-of-each-stage). The request makes 1 LLM call, or 2 when stored edges need to be compared, plus 1 embeddings request, or 2 when the [candidate search](#candidate-facts-load_candidates) also embeds the endpoints of the new facts (only for a dataset that already exists). If any step before the commit fails, nothing is stored. If the vector write after the commit fails, the request returns `502` but the artifact, entities and edges are already saved; the vectors are written again the next time the entities and facts are mentioned.

### Choosing the model of each stage

The LLM-invoking stages of the pipeline can each run on a different model, for instance a stronger model to extract and a cheaper one to resolve. The caller picks them per request with `models`, a map from a stage name to a model name:

```json
{"user_id": "u1", "text": "Alice works at Acme.", "models": {"extract_graph": "claude-sonnet-4-5", "resolve_edges": "claude-haiku-5-5"}}
```

| Stage | Step | Used for |
| ----- | ---- | -------- |
| `extract_graph` | 1, `Extract` (once per chunk of a long text) | the entities and edges the text yields |
| `resolve_edges` | 2, `resolveEdges` (only when there are candidates to compare) | the duplicate / contradiction check |
| `web_search` | 5, [web search](web-search.md) (only with `web_search`) | the model that runs the web search tool (it must support it); the briefings are extracted on `extract_graph` |
| `resolve_series` | 6, [timeseries fetch](timeseries.md#providers) (only with `timeseries.fetch`) | the LLM call that decides which timeseries provider is used for which entity |
| `extract_image` | the [image pipeline](images.md) | reading the image (applies only to a request with `image`) |

The rules:

- `models` is optional, and so is every stage in it. **A stage with no model (missing, or an empty string) uses the default model** of the server's LLM client (`claude-haiku-5-5`). So `{"models": {"extract_graph": "m"}}` changes only the extraction; `resolve_edges` stays on the default. A request without `models` runs everything on the default.
- The choice is per request: nothing is stored server-side, and the next call starts from the defaults again.
- A model name is any string of at most 128 bytes without whitespace or control characters; an unknown stage is a `400`. gligabao does not check that the provider knows the model: one it does not know fails the LLM call (`502`, nothing stored). The price of a model that is not in the price table (see `[prices]` in `gligabao.toml`) is recorded as 0.
- A stage in `models` that the request does not run is ignored (`resolve_edges` when there is nothing to compare with, `web_search` without the `web_search` flag, `resolve_series` without `timeseries.fetch`, `extract_image` for a text, `extract_graph` and `resolve_edges` for an image).
- Every call is recorded in `gb_llm_calls` with the model that answered, so the cost of each stage is known; an [eval trajectory](evals.md) shows the model of each LLM step (`model_name`) and the requested `models` in `extra.input`.

**The model of the knowledge.** The model that runs the `extract_graph` stage is the model of what it extracts: every entity and edge of the extraction carries it as `model_id` (see [graph.md](graph.md#models)), and it is registered in `gb_models` if it is new. It is the model name as the provider's API reports it (a dated snapshot, say, when the request named an alias), never anything the LLM wrote in its output. The `resolve_edges` model only decides which new facts are duplicates or contradictions, so it does not appear on the knowledge: an edge's `model_id` is the model that extracted it, even if another model decided that an older edge was contradicted. For a chunked text each chunk is extracted with the same `extract_graph` model, so all its entities and edges carry it. A fact that is merged into a stored edge keeps that edge's `model_id`, and a stored entity keeps its own (see [graph.md](graph.md#entities)), so one dataset can hold knowledge from several models when the calls used different ones.

**From Go**: `memory_core.WithModels(memory_core.Models{memory_core.StageExtractGraph: "m"})` is an option of `memory_remember.Remember` and `memory_image.RememberImage`; `memory_core.ResolveModels` validates a user-supplied map (the HTTP handler does). **From an eval**: see [evals.md](evals.md#models) and [`evals/README.md`](../evals/README.md).

### Candidate facts (`load_candidates`)

Before resolving, `Remember` loads the **candidates**: the facts already stored in the dataset that the new facts might repeat or contradict (`loadCandidates`). In an eval [trajectory](evals.md) this is the `load_candidates` tool step, which lists them and how each was found.

**What qualifies.** A stored edge is a candidate if it

- belongs to the same user's dataset (nothing is read across datasets),
- is still live (`expired_at IS NULL`; a fact already superseded is never compared again),
- touches (as source or target) an entity that is an endpoint of one of the new edges, found in either of two ways (below).

They are returned newest first, capped at 50 (`maxCandidates`). A dataset that does not exist yet has none. Example: the new fact "Alice lives in Berlin" has the endpoints Alice and Berlin, so every live fact about Alice or Berlin is a candidate, including "Alice lives in Paris".

**What they are used for.** They are the only stored facts the resolution looks at. Exact repeats are merged in Go; the other new facts and the candidates go to the LLM, which flags semantic duplicates and contradictions (see [temporal.md](temporal.md)). With no candidates there is no resolution LLM call. Anything not loaded here can never be merged or invalidated by this call.

**Two lookups, merged by edge ID.**

1. *By name* (always): a SQL lookup by normalized entity name (lowercased, whitespace-collapsed) of the endpoints (`graph.LoadCandidateEdges`).
2. *By similarity* (only with `WithVectors`, i.e. an OpenAI key): each endpoint is embedded as an entity is (`Name (type): description`; type `other` if the extraction did not list it) and searched in the dataset's entity index (`dataset-<id>-entities`). The 3 closest stored entities per endpoint with a cosine similarity of at least 0.75 are kept, except one with the same normalized name as the endpoint (the name lookup has it). The live facts touching those entities are loaded the same way as in 1. This finds aliases: a text saying "Alicia" finds the stored "Alice Smith" and her facts.

The two result sets are merged by edge ID: an edge found by both appears once, the merged list is sorted newest first and cut to 50 (so an alias match can lose its place to newer facts). In the trajectory each fact has `found_by`: `name`, `similar` or `name+similar`.

The similarity lookup costs 1 embeddings request (recorded as a call like any other embedding spend, and shown as the `search_similar_entities` step with its tokens and cost) and is only an aid: the vector store is not updated atomically with the database (see [vectors.md](vectors.md#what-remember-writes)) and its metadata has no validity, so the matched entities are only used to look up their facts in the database, which filters to live edges. If the embedding or the search fails, the request does **not** fail: a warning is logged, the step records the error and the candidates are those of the names alone. The threshold (0.75) and `TopK` (3) are first guesses to tune with the evals.

Remaining limits:

- Edges are not searched by similarity: a paraphrased fact about entities that match neither by name nor by similarity is not a candidate.
- A busy entity can still push old facts out of the newest-first cap of 50.
- The endpoints are embedded in a request of their own, and `Store` embeds the entities and edges again afterwards. Sharing one request is possible but not done yet.

#### Next step: similarity search over edges

Querying the edge index (`dataset-<id>-edges`) with the new facts' descriptions would rank candidates by relevance instead of recency, so the cap of 50 keeps the right facts, and would reach paraphrases that share no entity. Worth adding if the evals show the cap or the missing reach losing facts; it would join the same merge by edge ID (`found_by` would get a third value).

### Normalization and deduplication

- Entity `type` is lowercased (empty becomes `other`).
- Edge `relation` is lowercased with spaces replaced by `_`.
- Entities are unique on (lowercased, whitespace-collapsed name, type). Re-submitting an entity updates its description only if the new description is non-empty.
- Edge endpoints resolve to an entity from this text, else a stored entity of any type with the same name, else a new entity of type `other`.
- A new edge with the same source, target and relation as a stored edge that is still true is a duplicate and is not inserted again.
- Sending the same text twice adds a second `gb_artifacts` row (linking the existing entities and edges to it, and to its session if it has one) but no duplicate entities or edges, and no duplicate vectors: the existing ones are overwritten.

## Schema

All tables are prefixed with `gb_` (see `internal/db/relational/{sqlite,postgres}/db_schema.go` for datasets and `internal/db/graph/{sqlite,postgres}/db_schema.go` for the graph tables).

| Table | Columns |
| ----- | ------- |
| `gb_datasets` | `id`, `name`, `description`, `tags` (JSON), `created_at`, `deleted`, `deleted_at`; unique `name` among non-deleted |
| `gb_artifacts` | `id`, `dataset_id` → `gb_datasets`, `kind` (`knowledge` (default) or `episode`; `web_search` for a [web search](web-search.md) result and `series_observation` for a [series observation](timeseries.md#observations), which only the pipeline creates), `text`, `session_id` (NULL if none), `parent_id` → `gb_artifacts` (NULL unless the artifact is a [chunk](chunking.md) of another, or a web search: then the artifact the searched entity came from; or a series observation: the artifact that anchors the series), `chunk_index`, `start_offset`, `end_offset`, `tokens` (only for a chunk), `query` (only for a web search: the query that was issued; for a series observation: the series and window), `series_id` (only for a series observation: `gb_series`), `valid_at`, `created_at`; unique (`parent_id`, `chunk_index`) |
| `gb_entities` | `id`, `dataset_id` → `gb_datasets`, `name`, `name_key`, `type`, `description`, `created_at`, `updated_at`; unique (`dataset_id`, `name_key`, `type`) |
| `gb_edges` | `id`, `dataset_id` → `gb_datasets`, `source_id` → `gb_entities`, `target_id` → `gb_entities`, `relation`, `description`, `valid_at`, `invalid_at`, `created_at`, `expired_at`, `artifact_id` → `gb_artifacts` (the creating artifact) |
| `gb_edge_artifacts` | `edge_id` → `gb_edges`, `artifact_id` → `gb_artifacts`; primary key (`edge_id`, `artifact_id`) |
| `gb_entity_artifacts` | `entity_id` → `gb_entities`, `artifact_id` → `gb_artifacts`; primary key (`entity_id`, `artifact_id`) |
| `gb_entity_sessions` | `entity_id` → `gb_entities`, `session_id`; primary key (`entity_id`, `session_id`) |
| `gb_edge_sessions` | `edge_id` → `gb_edges`, `session_id`; primary key (`edge_id`, `session_id`) |
| `gb_series`, `gb_series_points`, `gb_series_anchors` | The [timeseries](timeseries.md) and what they are anchored to (`gb_artifacts.series_id` links a [series observation](timeseries.md#observations) to its series) |
| `gb_items` | `id`, `name`, `created_at` (placeholder) |

Foreign keys are enabled on every connection. Deleting an entity cascades to its edges and its `gb_entity_artifacts` links, and deleting an edge removes its `gb_edge_artifacts` links; deleting an artifact sets `artifact_id` to NULL on its edges and removes its `gb_edge_artifacts` and `gb_entity_artifacts` links. Deleting an entity or an edge also removes its `gb_entity_sessions` or `gb_edge_sessions` links; deleting an artifact leaves them (they record that the session discovered the entity or fact, not which artifact did).

**Why edges have a link table too.** (More on artifacts and their links in [artifacts.md](artifacts.md).) `gb_edges.artifact_id` is only the artifact that created the edge. When a later artifact states the same fact, the new edge is recognized as a duplicate (exactly, or by the LLM) and merged into the stored one instead of being inserted, so without a link table the fact that this artifact also supports it would be lost. `gb_edge_artifacts` holds the creating artifact and every artifact whose duplicate was merged in, so a fact's full provenance is kept (how many sources back it, which texts to cite). Contradicted edges are not linked: a contradiction is not a restatement.

**Why entities have a link table.** An edge is created by exactly one artifact, so it carries a plain `artifact_id`. An entity is not: entities are deduplicated per dataset on (`name_key`, `type`), so "Alice" mentioned in artifacts 1, 5 and 9 is one row, and one `artifact_id` column could only remember where she first appeared. The many-to-many `gb_entity_artifacts` table records every artifact an entity was inferred from, so you can ask which artifacts ground an entity and which entities an artifact contributes to, and an artifact can be removed without losing entities that other artifacts still support.

**Why sessions have link tables.** A session is a label on a stream of artifacts, and an entity or fact can be discovered in many sessions. Because entities and edges are deduplicated, the session that merely repeats one writes no row for it; `gb_entity_sessions` and `gb_edge_sessions` keep that connection anyway. See [sessions.md](sessions.md).

The vectors are not in these tables: they live in the vector store, isolated per dataset, one vector dataset per index (see [vectors.md](vectors.md#datasets)). They are keyed by the `id` of the entity or edge row.

Existing database files are not migrated when the schema changes: delete the file and the server recreates it.

## Configuration

- `ANTHROPIC_API_KEY`: required for this endpoint. If missing, the server still starts and the endpoint returns 503.
- `chunk_size` (default 1024 tokens) and `chunk_overlap_percent` (default 20): when and how a long text is chunked, see [chunking.md](chunking.md).
- Web search (`web_search`) is available exactly when `ANTHROPIC_API_KEY` is set; there is no other setting. See [web-search.md](web-search.md).
- `OPENAI_API_KEY` (or `openai_api_key` in `gligabao.toml`): used to embed entities, edges and chunks. Optional: without it the endpoint works and stores the graph, but no vectors, and the server logs a warning at startup.
- `embedding_dimensions` (default 1536) is the size of the embeddings; `vector_store` selects where they go (`chromem`, or `pinecone`). See [vectors.md](vectors.md).
- `gligabao serve --db <path>` selects the SQLite file (default `bin/gligabao.db`).

## Example

```sh
export ANTHROPIC_API_KEY=...
export OPENAI_API_KEY=...    # optional: embeds the entities and edges
./run_build.sh
./bin/gligabao serve &
curl -s localhost:7878/v1/gb/remember \
  -d '{"user_id":"u1","text":"Alice works at Acme and lives in Paris."}'
```

## Limitations

- No authentication.
- Web search is opt-in per call, searches at most 3 key entities, and is a heuristic and not free (a fee per search); see [web-search.md](web-search.md#limitations).
- Images are only read and stored (see [images.md](images.md)); they add nothing to the graph yet, and an image the dataset already holds (same SHA-256) is answered from its stored extraction without an LLM call.
- The database and the vector store are not updated atomically (see [vectors.md](vectors.md#what-remember-writes)), and there is no re-embedding of entities and edges stored before embedding was configured.
- The embeddings are not used by `recall` yet, but the entity embeddings do help pick the [candidate facts](#candidate-facts-load_candidates) in `remember` (aliases); edge embeddings do not, and at most 50 candidates are compared.
- A very long text is chunked, so it no longer exceeds the LLM's output limit as a whole; a single chunk whose extraction is truncated still fails the request with 502. For a chunked text a failure while storing a chunk leaves the chunks stored before it (see [chunking.md](chunking.md#each-chunk-is-extracted-individually)).
