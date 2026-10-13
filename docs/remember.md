# POST /v1/gb/remember

Takes a block of text (or, with `image`, an image: see [images.md](images.md)), stores it as an [artifact](artifacts.md), uses an LLM to extract a small knowledge graph (entities and edges) whose edges are bi-temporal facts, stores everything in the database, and embeds the entities and edges into the dataset's [vector datasets](vectors.md#datasets). The temporal model is described in [temporal.md](temporal.md).

## Request

```
POST /v1/gb/remember
content-type: application/json

{"user_id": "u1", "text": "Alice works at Acme and lives in Paris.", "kind": "episode", "reference_time": "2025-01-31T09:00:00Z"}
```

- `user_id` (required): the user who owns the dataset. `400` if missing.
- `text` (required unless `image` is given): non-empty string, at most 1 MiB.
- `image` (optional): a base64-encoded image, instead of `text`. The request then goes through the separate [image pipeline](images.md) (OCR of the title and text blocks, and a theme such as `plot` or `graph`), with a different response; `text` and `kind` must not be given with it.
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
  "invalidated_edges": [],
  "embedded_entities": 3,
  "embedded_edges": 2
}
```

- `artifact_id`, `kind`, `valid_at`, `created_at`: the artifact, its kind, and its event time (the `reference_time`) and ingestion time.
- `entities` are what the LLM extracted from this text (after normalization).
- `edges` are the facts from this text in their stored state; if a new edge repeated a stored one, the stored edge is returned. Edge fields `invalid_at`, `expired_at` are omitted when null.
- `invalidated_edges` lists IDs of edges closed because of this artifact (see [temporal.md](temporal.md)). Always an array.
- `embedded_entities` and `embedded_edges` are the number of vectors written to the dataset's entity and edge indexes (see [Embed](#how-it-works)). Both are omitted when nothing was embedded (no `OPENAI_API_KEY`). `embedded_entities` counts the entities the text mentions, including those that already existed, and `embedded_edges` the facts it states, including repeated ones; they can be more than the number of new rows.

Errors are returned as `{"error": "..."}`:

| Status | Meaning |
| ------ | ------- |
| 400 | Invalid JSON body, `text` is empty, `kind` is not `knowledge` or `episode`, or `reference_time` is not RFC 3339 |
| 502 | An LLM call failed, or its output could not be parsed or was truncated; or embedding failed (nothing stored); or the vectors could not be written to the vector store (after the database was saved, see [Embed](#how-it-works)) |
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
2. **Resolve edges** (`resolveEdges`, 1 LLM call only if needed): new edges are compared with the [candidate facts](#candidate-facts-load_candidates): stored live edges that touch the same entities. Exact repeats are detected without the LLM; the model flags remaining duplicates and contradictions.
3. **Store** (`Store`): everything is written in a single transaction:
   - the raw text and its reference time go into `gb_artifacts` (with its `kind`);
   - entities are upserted into `gb_entities` and linked to the artifact in `gb_entity_artifacts`, including stored entities that an edge names but the extraction did not list;
   - edges are inserted into `gb_edges`, anchored to the artifact that created them (`artifact_id`) and linked to it in `gb_edge_artifacts`; a duplicate of a stored edge is not inserted but the new artifact is added to that edge's `gb_edge_artifacts` links; duplicates are merged and contradicted edges are closed (see [temporal.md](temporal.md)).

4. **Embed** (`WithVectors`, 1 embeddings request, only if an OpenAI key is configured): the entities this text mentions and the edges it states are embedded together with `text-embedding-3-small`: an entity as `Name (type): description`, an edge as its description. This happens inside step 3, after the rows are written and before the transaction commits, so a failure stores nothing. Right after the commit the vectors are written, with `Upsert`, to the dataset's two vector datasets: `dataset-<id>-entities` (vector `entity-<entity id>`) and `dataset-<id>-edges` (vector `edge-<edge id>`). Vectors are keyed by ID, so remembering an entity or fact again overwrites its vector. Details, metadata and failure handling: [vectors.md](vectors.md#what-remember-writes).

The request makes 1 LLM call, or 2 when stored edges need to be compared, plus 1 embeddings request, or 2 when the [candidate search](#candidate-facts-load_candidates) also embeds the endpoints of the new facts (only for a dataset that already exists). If any step before the commit fails, nothing is stored. If the vector write after the commit fails, the request returns `502` but the artifact, entities and edges are already saved; the vectors are written again the next time the entities and facts are mentioned.

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
- Sending the same text twice adds a second `gb_artifacts` row (linking the existing entities and edges to it) but no duplicate entities or edges, and no duplicate vectors: the existing ones are overwritten.

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

**Why edges have a link table too.** (More on artifacts and their links in [artifacts.md](artifacts.md).) `gb_edges.artifact_id` is only the artifact that created the edge. When a later artifact states the same fact, the new edge is recognized as a duplicate (exactly, or by the LLM) and merged into the stored one instead of being inserted, so without a link table the fact that this artifact also supports it would be lost. `gb_edge_artifacts` holds the creating artifact and every artifact whose duplicate was merged in, so a fact's full provenance is kept (how many sources back it, which texts to cite). Contradicted edges are not linked: a contradiction is not a restatement.

**Why entities have a link table.** An edge is created by exactly one artifact, so it carries a plain `artifact_id`. An entity is not: entities are deduplicated per dataset on (`name_key`, `type`), so "Alice" mentioned in artifacts 1, 5 and 9 is one row, and one `artifact_id` column could only remember where she first appeared. The many-to-many `gb_entity_artifacts` table records every artifact an entity was inferred from, so you can ask which artifacts ground an entity and which entities an artifact contributes to, and an artifact can be removed without losing entities that other artifacts still support.

The vectors are not in these tables: they live in the vector store, isolated per dataset, one vector dataset per index (see [vectors.md](vectors.md#datasets)). They are keyed by the `id` of the entity or edge row.

Existing database files are not migrated when the schema changes: delete the file and the server recreates it.

## Configuration

- `ANTHROPIC_API_KEY`: required for this endpoint. If missing, the server still starts and the endpoint returns 503.
- `OPENAI_API_KEY` (or `openai_api_key` in `gligabao.toml`): used to embed entities and edges. Optional: without it the endpoint works and stores the graph, but no vectors, and the server logs a warning at startup.
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
- Images are only read and stored (see [images.md](images.md)); they add nothing to the graph yet, and an image the dataset already holds (same SHA-256) is answered from its stored extraction without an LLM call.
- The database and the vector store are not updated atomically (see [vectors.md](vectors.md#what-remember-writes)), and there is no re-embedding of entities and edges stored before embedding was configured.
- The embeddings are not used by `recall` yet, but the entity embeddings do help pick the [candidate facts](#candidate-facts-load_candidates) in `remember` (aliases); edge embeddings do not, and at most 50 candidates are compared.
- Very long input can exceed the LLM's output limit; the request then fails with 502 and nothing is stored.
